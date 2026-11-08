package retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
)

func TestSemanticEvidenceAcrossBrains(t *testing.T) {
	var queries atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprintf(w, `{"models":[{"name":"fixture:latest","digest":%q,"size":1,"details":{"format":"gguf","family":"nomic-bert"}}]}`, strings.Repeat("a", 64))
		case "/api/embed":
			var input struct {
				Texts []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
				return
			}
			var vectors [][]int
			for _, text := range input.Texts {
				switch {
				case strings.HasPrefix(text, "search_query: "):
					queries.Add(1)
					vectors = append(vectors, []int{1, 0})
				case strings.HasPrefix(text, "search_document: ") && !strings.Contains(text, "Unrelated"):
					vectors = append(vectors, []int{3, 1})
				default:
					vectors = append(vectors, []int{0, 1})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
		default:
			http.NotFound(w, r)
		}
	}))
	defer model.Close()
	t.Setenv("BRAINDEX_OLLAMA_PORT", strconv.Itoa(model.Listener.Addr().(*net.TCPAddr).Port))

	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sources := []struct{ id, title, text string }{
		{"sales", "Synthetic founder notes", "Interview buyers about budget approval and procurement."},
		{"markets", "Synthetic market newsletter", "Credit tightens and firms defer discretionary spending."},
		{"other", "Unrelated notebook", "Unrelated recipes for dinner."},
		{"denied", "Private", "Keep these relevant records private."},
	}
	for _, item := range sources {
		if err := store.CreateBrain(library.Brain{ID: item.id}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.PutSource(library.Source{Title: item.title, Passages: []library.Passage{{Text: item.text, Locator: "page 2"}}}, []string{item.id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetBrainAccess(store, "denied", false); err != nil {
		t.Fatal(err)
	}
	if err := SaveRule(store, Rule{ID: "planning", Phrases: []string{"commercial planning"}, Brains: []string{"sales", "markets"}}); err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild(store); err != nil {
		t.Fatal(err)
	}
	if err := index.RebuildEmbeddings(context.Background(), store, "fixture", 0.55); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	health := index.InspectEmbeddings(context.Background(), store, snapshot.Sources, true)
	if health.State != "current" || health.Model != "fixture:latest" || health.Runtime != "ready" || queries.Load() != 0 {
		t.Fatalf("diagnostics should verify metadata without embedding source text: %+v", health)
	}
	request := Request{Query: "Commercial planning during recession?", Limit: 1}
	result, err := Context(store, request)
	if err != nil || result.Retrieval != "hybrid" || len(result.Brains) != 2 || queries.Load() != 1 {
		t.Fatalf("expected two independently ranked brains and one query embedding: %+v, %v, %d calls", result, err, queries.Load())
	}
	for i, brain := range result.Brains {
		if brain.ID != sources[i].id || brain.Status != "hits" || len(brain.Excerpts) != 1 {
			t.Fatalf("unexpected brain outcome: %+v", brain)
		}
		citation := brain.Excerpts[0]
		if citation.Match != "semantic" || citation.Title != sources[i].title || citation.Locator != "page 2" || citation.Revision == "" {
			t.Fatalf("incorrect citation: %+v", citation)
		}
	}
	request.AllBrains = true
	result, err = Context(store, request)
	if err != nil || len(result.Brains) != 4 || result.Brains[0].Status != "denied" || result.Brains[2].Status != "no_relevant_passages" {
		t.Fatalf("library-wide search failed to preserve scope or policy: %+v, %v", result, err)
	}

	if _, err := store.PutSource(library.Source{Title: "New source", Passages: []library.Passage{{Text: "The recession changes forecasts.", Locator: "line 1"}}}, []string{"sales"}); err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild(store); err != nil {
		t.Fatal(err)
	}
	request.AllBrains = false
	result, err = Context(store, request)
	if err != nil || result.Retrieval != "keyword" || result.EmbeddingStatus != "stale" || result.Brains[0].Status != "hits" {
		t.Fatalf("stale embeddings should fall back to keyword search: %+v, %v", result, err)
	}
	request.Mode = "semantic"
	if _, err := Context(store, request); err == nil {
		t.Fatal("explicit semantic retrieval accepted a stale index")
	}
}
