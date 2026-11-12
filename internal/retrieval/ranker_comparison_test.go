package retrieval

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
)

func TestRankerComparison(t *testing.T) {
	var queries atomic.Int32
	var digest atomic.Value
	digest.Store(rankerModelDigest)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprintf(w, `{"models":[{"name":"fixture:latest","digest":%q,"size":1,"details":{"format":"gguf","family":"nomic-bert"}}]}`, digest.Load())
		case "/api/embed":
			var payload struct{ Input []string }
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
				http.Error(w, "bad input", http.StatusBadRequest)
				return
			}
			vectors := make([][]int, 0, len(payload.Input))
			for _, input := range payload.Input {
				switch input {
				case "search_query: budget review":
					queries.Add(1)
					vectors = append(vectors, []int{1, 0})
				case "search_document: Deep value evidence.":
					vectors = append(vectors, []int{4, 1})
				case "search_document: Distractor.",
					"search_document: Weak unrelated passage.":
					vectors = append(vectors, []int{3, 4})
				default:
					vectors = append(vectors, []int{0, 1})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("BRAINDEX_OLLAMA_PORT", strconv.Itoa(server.Listener.Addr().(*net.TCPAddr).Port))

	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sales", "noise", "empty", "denied"} {
		if err := store.CreateBrain(library.Brain{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, sample := range []struct{ id, brain, body string }{
		{"lexical", "sales", "Budget review case."},
		{"strong", "sales", "Deep value evidence."},
		{"weak", "sales", "Distractor."},
		{"noise-only", "noise", "Weak unrelated passage."},
		{"private", "denied", "Budget review private."},
	} {
		if _, err := store.PutSource(library.Source{
			ID: sample.id, Title: sample.id, Passages: []library.Passage{{Text: sample.body, Locator: "page 2"}},
		}, []string{sample.brain}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetBrainAccess(store, "denied", false); err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild(store); err != nil {
		t.Fatal(err)
	}
	run := func(request Request) Result {
		t.Helper()
		result, err := ContextWithContext(t.Context(), store, request)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	assertUnavailable := func(request Request, reason string) {
		t.Helper()
		result := run(request)
		comparison := result.RankerComparison
		if comparison == nil || comparison.Status != "unavailable" || comparison.Reason != reason || len(comparison.Brains) != 0 {
			t.Fatalf("comparison = %+v, want unavailable %q", comparison, reason)
		}
	}

	request := Request{Query: "budget review", Brains: []string{"sales", "noise", "empty", "denied"}, CompareRanker: true}
	assertUnavailable(request, "embeddings_missing")
	assertUnavailable(Request{Query: request.Query, CompareRanker: true}, "unmatched")
	assertUnavailable(Request{Query: request.Query, Brains: []string{"denied", "empty"}, CompareRanker: true}, "no_searchable_brains")
	assertUnavailable(Request{Query: request.Query, Brains: request.Brains, Mode: "keyword", CompareRanker: true}, "keyword_mode")

	rebuild := func() {
		t.Helper()
		if err := index.RebuildEmbeddings(t.Context(), store, "fixture", 0.55); err != nil {
			t.Fatal(err)
		}
	}
	rebuild()
	baselineRequest := request
	baselineRequest.CompareRanker = false
	baseline := run(baselineRequest)
	encoded, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.RankerComparison != nil || strings.Contains(string(encoded), "ranker_comparison") {
		t.Fatal("default response contains comparison")
	}
	before := queries.Load()
	result := run(request)
	if queries.Load() != before+1 {
		t.Fatalf("comparison should reuse query inference, got %d calls", queries.Load()-before)
	}
	comparison := result.RankerComparison
	if comparison == nil || comparison.Status != "compared" || comparison.Model != "fixture:latest" ||
		comparison.ModelDigest != rankerModelDigest || comparison.MinimumCosine != rankerMinimumCosine ||
		len(comparison.Brains) != 4 {
		t.Fatalf("incorrect comparison metadata: %+v", comparison)
	}
	result.RankerComparison = nil
	if !reflect.DeepEqual(baseline, result) {
		t.Fatalf("comparison changed the baseline: %+v vs %+v", baseline, result)
	}
	sales := comparison.Brains[0]
	if sales.ID != "sales" || sales.Status != "hits" || sales.Removed != 1 || len(sales.Excerpts) != 2 ||
		sales.Excerpts[0].SourceID != "strong" || sales.Excerpts[1].SourceID != "lexical" {
		t.Fatalf("wrong ranked evidence: %+v", sales)
	}
	for _, retained := range sales.Excerpts {
		for _, original := range result.Brains[0].Excerpts {
			if retained.SourceID == original.SourceID && !reflect.DeepEqual(retained, original) {
				t.Fatalf("retained citation changed: %+v vs %+v", retained, original)
			}
		}
	}
	for i, want := range []string{"no_relevant_passages", "unavailable", "denied"} {
		other := comparison.Brains[i+1]
		if other.Status != want || len(other.Excerpts) != 0 || other.ID != request.Brains[i+1] ||
			other.Removed != len(result.Brains[i+1].Excerpts) {
			t.Fatalf("comparison changed scope or retained extra evidence: %+v", other)
		}
	}
	*sales.Excerpts[0].Similarity = 0
	for _, original := range result.Brains[0].Excerpts {
		if original.SourceID == "strong" && *original.Similarity == 0 {
			t.Fatal("comparison shares a score pointer with the baseline")
		}
	}

	assertUnavailable(Request{Query: request.Query, Brains: request.Brains, Mode: "semantic", CompareRanker: true}, "semantic_mode")
	digest.Store(strings.Repeat("b", 64))
	assertUnavailable(request, "embeddings_stale")
	rebuild()
	assertUnavailable(request, "unsupported_model")
	t.Setenv("BRAINDEX_OLLAMA_PORT", "invalid")
	assertUnavailable(request, "embeddings_unavailable")
}
