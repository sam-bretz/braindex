package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sam-bretz/braindex/internal/inbox"
	"github.com/sam-bretz/braindex/internal/library"
	"github.com/sam-bretz/braindex/internal/retrieval"
)

func TestInboxSuggestLocalScoresAndBoundedSelectedData(t *testing.T) {
	store := inboxFixture(t)
	if err := store.CreateBrain(library.Brain{ID: "denied", Name: "denied brain label"}); err != nil {
		t.Fatal(err)
	}
	if err := retrieval.SetBrainAccess(store, "denied", false); err != nil {
		t.Fatal(err)
	}
	for text, brains := range map[string][]string{
		"sales profile example":   {"sales"},
		"product profile example": {"product"},
		"denied private passage":  {"denied"},
		"denied shared passage":   {"sales", "denied"},
	} {
		if _, err := store.PutSource(library.Source{Title: "/arbitrary/unrelated/metadata-path", Passages: []library.Passage{{Text: text, Locator: "line 1"}}}, brains); err != nil {
			t.Fatal(err)
		}
	}

	match := captureInboxFixture(t, store, "note", "selected strong note")
	ambiguous := captureInboxFixture(t, store, "note", "selected ambiguous note")
	weak := captureInboxFixture(t, store, "note", "selected weak note")
	long := captureInboxFixture(t, store, "note", strings.Repeat("ë", maxSuggestionNote/2+10)+"uncaptured suffix")
	unselected := captureInboxFixture(t, store, "note", "unselected private note")
	symlink := captureInboxFixture(t, store, "note", "selected symlink note")
	attachmentPath := filepath.Join(t.TempDir(), "selected-private-file.txt")
	if err := os.WriteFile(attachmentPath, []byte("private attachment data"), 0600); err != nil {
		t.Fatal(err)
	}
	attachment := captureInboxFixture(t, store, "file", attachmentPath)
	link := captureInboxFixture(t, store, "url", "https://example.org/private-capture")
	if err := os.Remove(inbox.ContentPath(store, symlink)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inbox.ContentPath(store, unselected), inbox.ContentPath(store, symlink)); err != nil {
		t.Fatal(err)
	}
	if _, err := inbox.Propose(store, match.ID, inbox.Proposal{Digest: "existing", Rationale: "prior review", Brains: []string{"sales"}}); err != nil {
		t.Fatal(err)
	}
	before, err := inbox.Get(store, match.ID)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var modelInputs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprintf(w, `{"models":[{"name":"nomic-embed-text:latest","digest":%q,"size":42,"details":{"format":"gguf","family":"nomic-bert"}}]}`, strings.Repeat("a", 64))
		case "/api/embed":
			var request struct {
				Model    string   `json:"model"`
				Input    []string `json:"input"`
				Truncate bool     `json:"truncate"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "nomic-embed-text:latest" || request.Truncate {
				http.Error(w, "invalid model request", http.StatusBadRequest)
				return
			}
			mu.Lock()
			modelInputs = append(modelInputs, request.Input...)
			mu.Unlock()
			vectors := make([][]int, 0, len(request.Input))
			for _, input := range request.Input {
				switch {
				case strings.HasPrefix(input, "search_document: ") && strings.Contains(input, "sales profile example"):
					vectors = append(vectors, []int{1, 0, 0})
				case strings.HasPrefix(input, "search_document: ") && strings.Contains(input, "product profile example"):
					vectors = append(vectors, []int{0, 1, 0})
				case input == "search_query: selected ambiguous note":
					vectors = append(vectors, []int{1, 1, 0})
				case input == "search_query: selected weak note":
					vectors = append(vectors, []int{0, 0, 1})
				case input == "search_query: selected strong note",
					input == "search_query: "+strings.Repeat("ë", maxSuggestionNote/2):
					vectors = append(vectors, []int{1, 0, 0})
				default:
					http.Error(w, "unexpected fixture content", http.StatusBadRequest)
					return
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRAINDEX_OLLAMA_PORT", endpoint.Port())

	raw := runInboxFixture(t, store, nil, "suggest", match.ID, ambiguous.ID, weak.ID, long.ID, symlink.ID, attachment.ID, link.ID, "--model", "nomic-embed-text")
	var report inboxSuggestionReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatal(err)
	}
	expect := []struct {
		item          inbox.Item
		reason, brain string
		hasScore      bool
		truncated     bool
	}{
		{match, "best_match", "sales", true, false},
		{ambiguous, "ambiguous_match", "", true, false},
		{weak, "weak_match", "", true, false},
		{long, "best_match", "sales", true, true},
		{symlink, "item_unavailable", "", false, false},
		{attachment, "unsupported_kind", "", false, false},
		{link, "unsupported_kind", "", false, false},
	}
	if len(report.Suggestions) != len(expect) || report.Model != "nomic-embed-text" {
		t.Fatalf("unexpected report: %+v", report)
	}
	for i, want := range expect {
		got := report.Suggestions[i]
		if got.ItemID != want.item.ID || got.Reason != want.reason || got.BrainID != want.brain ||
			(got.Score != nil) != want.hasScore || (got.RunnerUpScore != nil) != want.hasScore ||
			got.Truncated != want.truncated || (got.Status == "suggested") != (want.brain != "") {
			t.Fatalf("suggestion %d = %+v, want %+v", i, got, want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(modelInputs) != 6 {
		t.Fatalf("model saw %d inputs, want 2 allowed profiles and 4 selected notes", len(modelInputs))
	}
	for _, forbidden := range []string{"denied", "unselected", "arbitrary", "attachment", "private-capture", "uncaptured suffix", symlink.ID} {
		if strings.Contains(strings.Join(modelInputs, "\n"), forbidden) {
			t.Errorf("model received disallowed input containing %q", forbidden)
		}
	}
	after, err := inbox.Get(store, match.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("suggestion changed the item: before %+v, after %+v, error %v", before, after, err)
	}
	if _, err := os.Stat(filepath.Join(store.MetaDir(), "embeddings.sqlite")); !os.IsNotExist(err) {
		t.Fatalf("read-only suggestion unexpectedly created an embeddings index: %v", err)
	}
}

func TestInboxSuggestAbstainsWithoutAllowedBrainsOrRuntime(t *testing.T) {
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBrain(library.Brain{ID: "denied"}); err != nil {
		t.Fatal(err)
	}
	if err := retrieval.SetBrainAccess(store, "denied", false); err != nil {
		t.Fatal(err)
	}
	note := captureInboxFixture(t, store, "note", "a selected note")
	t.Setenv("BRAINDEX_OLLAMA_PORT", "invalid-port")
	raw := runInboxFixture(t, store, nil, "suggest", note.ID, "--model", "nomic-embed-text")
	var report inboxSuggestionReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Suggestions) != 1 || report.Suggestions[0].Reason != "no_allowed_brains" ||
		report.Suggestions[0].Status != "abstained" || report.Suggestions[0].Score != nil {
		t.Fatalf("unexpected abstention: %+v", report)
	}
}
