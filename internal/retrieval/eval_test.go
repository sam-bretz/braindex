package retrieval

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
)

// TestRetrievalQuality is a small, labeled smoke benchmark. Keyword results
// are checked in CI. An optional loopback model reports a second baseline.
func TestRetrievalQuality(t *testing.T) {
	var fixture struct {
		Sources []struct {
			ID, Brain, Title, Text string
		}
		Queries []struct {
			ID, Question     string
			Brains, Expected []string
		}
	}
	data, err := os.ReadFile("testdata/retrieval-eval.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created := map[string]bool{}
	for _, sample := range fixture.Sources {
		if !created[sample.Brain] {
			if err := store.CreateBrain(library.Brain{ID: sample.Brain}); err != nil {
				t.Fatal(err)
			}
			created[sample.Brain] = true
		}
		_, err := store.PutSource(library.Source{ID: sample.ID, Title: sample.Title,
			Passages: []library.Passage{{Text: sample.Text, Locator: "line 1"}}}, []string{sample.Brain})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := index.Rebuild(store); err != nil {
		t.Fatal(err)
	}
	modes := []string{"keyword"}
	if model := os.Getenv("BRAINDEX_EVAL_MODEL"); model != "" {
		if err := index.RebuildEmbeddings(context.Background(), store, model, 0.55); err != nil {
			t.Fatal(err)
		}
		modes = append(modes, "auto")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			var latencies []time.Duration
			relevant, returned, found, negatives, emptyNegatives := 0, 0, 0, 0, 0
			for _, sample := range fixture.Queries {
				start := time.Now()
				result, err := Context(store, Request{Query: sample.Question, Brains: sample.Brains, Mode: mode, Limit: 3})
				latencies = append(latencies, time.Since(start))
				if err != nil {
					t.Fatal(err)
				}
				if mode == "auto" && result.Retrieval != "hybrid" {
					t.Fatalf("expected hybrid retrieval, got %s (%s)", result.Retrieval, result.EmbeddingStatus)
				}
				var seen []string
				for _, brain := range result.Brains {
					for _, excerpt := range brain.Excerpts {
						if excerpt.Locator == "" || excerpt.Revision == "" {
							t.Fatalf("uncitable result for %s", sample.ID)
						}
						if !slices.Contains(seen, excerpt.SourceID) {
							seen = append(seen, excerpt.SourceID)
						}
					}
				}
				hits := 0
				for _, id := range sample.Expected {
					if slices.Contains(seen, id) {
						hits++
					}
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
			if mode == "keyword" && (found < 5 || emptyNegatives != negatives) {
				t.Fatal("keyword retrieval regressed against the labeled fixture")
			}
		})
	}
}
