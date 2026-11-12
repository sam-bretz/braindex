package retrieval

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
)

const evalEmbeddingCutoff = 0.55

// TestRetrievalQuality compares each query against its labeled relevant sources
// and a per-mode distractor budget.
func TestRetrievalQuality(t *testing.T) {
	var fixture struct {
		Sources []struct {
			ID, Brain, Title, Text string
		}
		Queries []struct {
			ID, Question        string
			Brains, Expected    []string
			AllowedKeywordMiss  string `json:"allowed_keyword_miss"`
			MaxHybridIrrelevant int    `json:"max_hybrid_irrelevant"`
		}
	}
	readEvalFixture(t, "testdata/retrieval-eval.json", &fixture)
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]evalSource{}
	created := map[string]bool{}
	for _, sample := range fixture.Sources {
		if !created[sample.Brain] {
			if err := store.CreateBrain(library.Brain{ID: sample.Brain}); err != nil {
				t.Fatal(err)
			}
			created[sample.Brain] = true
		}
		sources[sample.ID] = putEvalSource(t, store, sample.Brain, sample.ID, sample.Title, "line 1", sample.Text)
	}
	if err := index.Rebuild(store); err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, requestMode, expectedMode string) {
		var latencies []time.Duration
		relevant, returned, found, negatives, emptyNegatives := 0, 0, 0, 0, 0
		for _, sample := range fixture.Queries {
			start := time.Now()
			result := runEvalQuery(t, store, sample.ID, sample.Question, sample.Brains, requestMode, expectedMode)
			latencies = append(latencies, time.Since(start))
			seen := make(map[string]bool)
			for _, brain := range result.Brains {
				for _, excerpt := range brain.Excerpts {
					checkEvalCitation(t, sample.ID, brain.ID, sources[excerpt.SourceID], excerpt)
					seen[excerpt.SourceID] = true
				}
			}
			hits := 0
			for _, id := range sample.Expected {
				if seen[id] {
					hits++
				} else if expectedMode != "keyword" || id != sample.AllowedKeywordMiss {
					t.Errorf("%s: missing relevant source %s", sample.ID, id)
				}
			}
			budget := 0
			if expectedMode == "hybrid" {
				budget = sample.MaxHybridIrrelevant
			}
			if irrelevant := len(seen) - hits; irrelevant > budget {
				t.Errorf("%s: returned %d irrelevant sources, maximum %d", sample.ID, irrelevant, budget)
			}
			if len(sample.Expected) == 0 {
				negatives++
				if len(seen) == 0 {
					emptyNegatives++
				}
			}
			relevant += len(sample.Expected)
			returned += len(seen)
			found += hits
			t.Logf("%s: %d/%d relevant sources, %d returned", sample.ID, hits, len(sample.Expected), len(seen))
		}
		slices.Sort(latencies)
		t.Logf("recall@3/brain: %d/%d, precision: %d/%d, empty negatives: %d/%d, median query: %s",
			found, relevant, found, returned, emptyNegatives, negatives, latencies[len(latencies)/2])
	}

	t.Run("keyword", func(t *testing.T) { run(t, "keyword", "keyword") })
	if model := os.Getenv("BRAINDEX_EVAL_MODEL"); model != "" {
		t.Run("hybrid_local", func(t *testing.T) {
			rebuildEvalEmbeddings(t, store, model)
			run(t, "auto", "hybrid")
		})
	}
}

type evalSource struct {
	brain string
	data  library.Source
}

func readEvalFixture(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func putEvalSource(t *testing.T, store *library.Store, brain, id, title, locator, text string) evalSource {
	t.Helper()
	source, err := store.PutSource(library.Source{
		ID: id, Title: title, Passages: []library.Passage{{Text: text, Locator: locator}},
	}, []string{brain})
	if err != nil {
		t.Fatal(err)
	}
	return evalSource{brain: brain, data: source}
}

func rebuildEvalEmbeddings(t *testing.T, store *library.Store, model string) {
	t.Helper()
	if err := index.RebuildEmbeddings(t.Context(), store, model, evalEmbeddingCutoff); err != nil {
		t.Fatal(err)
	}
	sources, err := store.Sources()
	if err != nil {
		t.Fatal(err)
	}
	health := index.InspectEmbeddings(t.Context(), store, sources, false)
	if health.State != "current" {
		t.Fatalf("embedding index state after rebuild: %s", health.State)
	}
	t.Logf("embedding model: %s, dimensions: %d, minimum cosine similarity: %.2f", health.Model, health.Dimensions, evalEmbeddingCutoff)
}

func runEvalQuery(t *testing.T, store *library.Store, id, question string, brains []string, mode, expectedMode string) Result {
	t.Helper()
	result, err := ContextWithContext(t.Context(), store, Request{Query: question, Brains: brains, Mode: mode, Limit: 3})
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	if result.Retrieval != expectedMode {
		t.Fatalf("%s: expected %s retrieval, got %s (%s)", id, expectedMode, result.Retrieval, result.EmbeddingStatus)
	}
	selected := make([]string, 0, len(result.Brains))
	for _, brain := range result.Brains {
		selected = append(selected, brain.ID)
	}
	if result.Routing.Scope != "explicit_brains" || !slices.Equal(selected, brains) {
		t.Fatalf("%s: scope %s searched %v, expected %v", id, result.Routing.Scope, selected, brains)
	}
	return result
}

func checkEvalCitation(t *testing.T, query, brain string, source evalSource, excerpt Excerpt) {
	t.Helper()
	if source.brain != brain || source.data.Title != excerpt.Title || source.data.Revision == "" ||
		source.data.Revision != excerpt.Revision || len(source.data.Passages) != 1 ||
		source.data.Passages[0] != (library.Passage{Text: excerpt.Text, Locator: excerpt.Locator}) {
		t.Errorf("%s: invalid citation from brain %s: %+v", query, brain, excerpt)
	}
}
