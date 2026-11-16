package retrieval

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
)

func TestComplementaryEvidence(t *testing.T) {
	type expectedPassage struct{ Source, Locator string }
	var fixture struct {
		Sources []struct {
			ID, Brain, Title, Locator, Text string
			Vector                          []int
		}
		Queries []struct {
			ID, Question         string
			Brains               []string
			Expected             map[string]expectedPassage
			Vector               []int
			MaxHybridIrrelevant  int `json:"max_hybrid_irrelevant"`
			MaxHybridExtraBrains int `json:"max_hybrid_extra_brains"`
		}
	}
	readEvalFixture(t, "testdata/complementary-evidence.json", &fixture)
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, brain := range []string{"sales", "markets", "evals", "operations", "empty"} {
		if err := store.CreateBrain(library.Brain{ID: brain}); err != nil {
			t.Fatal(err)
		}
	}
	vectors := map[string][]int{}
	sources := map[string]evalSource{}
	for _, source := range fixture.Sources {
		sources[source.ID] = putEvalSource(t, store, source.Brain, source.ID, source.Title, source.Locator, source.Text)
		vectors["search_document: "+source.Text] = source.Vector
	}
	for _, sample := range fixture.Queries {
		vectors["search_query: "+sample.Question] = sample.Vector
	}
	if err := index.Rebuild(store); err != nil {
		t.Fatal(err)
	}

	type runConfig struct {
		name, model string
	}
	runs := []runConfig{{name: "keyword"}, {name: "hybrid_fixture", model: "fixture"}}
	if model := os.Getenv("BRAINDEX_EVAL_MODEL"); model != "" {
		runs = append(runs, runConfig{name: "hybrid_local", model: model})
	}
	for _, run := range runs {
		t.Run(run.name, func(t *testing.T) {
			requestMode, wantMode := "keyword", "keyword"
			if run.model != "" {
				if run.name == "hybrid_fixture" {
					setFixtureEmbeddings(t, vectors)
				}
				rebuildEvalEmbeddings(t, store, run.model)
				requestMode, wantMode = "auto", "hybrid"
			}
			var expected, top1, top3, passages, irrelevant, irrelevantBrains, contributingBrains int
			for _, sample := range fixture.Queries {
				result := runEvalQuery(t, store, sample.ID, sample.Question, sample.Brains, requestMode, wantMode)
				expected += len(sample.Expected)
				var extraPassages, extraBrains int
				for _, brain := range result.Brains {
					want, relevant := sample.Expected[brain.ID]
					if len(brain.Excerpts) > 0 {
						contributingBrains++
						if !relevant {
							extraBrains++
						}
					}
					found := false
					for rank, excerpt := range brain.Excerpts {
						passages++
						checkEvalCitation(t, sample.ID, brain.ID, sources[excerpt.SourceID], excerpt)
						t.Logf("%s: %s #%d %s %s", sample.ID, brain.ID, rank+1, excerpt.SourceID, excerpt.Match)
						if relevant && !found && excerpt.SourceID == want.Source && excerpt.Locator == want.Locator {
							found = true
							top3++
							if rank == 0 {
								top1++
							}
						} else {
							extraPassages++
						}
					}
					if relevant && (!found || brain.Excerpts[0].SourceID != want.Source || brain.Excerpts[0].Locator != want.Locator) {
						t.Errorf("%s: %s should rank %s at %s first", sample.ID, brain.ID, want.Source, want.Locator)
					}
				}
				maxPassages, maxBrains := 0, 0
				if run.name == "hybrid_local" {
					maxPassages, maxBrains = sample.MaxHybridIrrelevant, sample.MaxHybridExtraBrains
				}
				if extraPassages > maxPassages || extraBrains > maxBrains {
					t.Errorf("%s: %d irrelevant passages and %d irrelevant contributing brains, maximum %d and %d",
						sample.ID, extraPassages, extraBrains, maxPassages, maxBrains)
				}
				irrelevant += extraPassages
				irrelevantBrains += extraBrains
			}
			t.Logf("per-brain top-1 coverage %d/%d, top-3 coverage %d/%d, irrelevant passages %d/%d, irrelevant contributing brains %d/%d",
				top1, expected, top3, expected, irrelevant, passages, irrelevantBrains, contributingBrains)
			if top1 != expected {
				t.Errorf("missing complementary evidence at rank 1")
			}
		})
	}
}

func setFixtureEmbeddings(t *testing.T, vectors map[string][]int) {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprintf(w, `{"models":[{"name":"fixture:latest","digest":%q,"size":1,"details":{"format":"gguf","family":"nomic-bert"}}]}`, strings.Repeat("b", 64))
		case "/api/embed":
			var input struct {
				Texts []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
				http.Error(w, "invalid fixture", http.StatusBadRequest)
				return
			}
			embeddings := make([][]int, 0, len(input.Texts))
			for _, text := range input.Texts {
				vector, ok := vectors[text]
				if !ok {
					t.Errorf("unexpected embedding input: %q", text)
					http.Error(w, "unexpected fixture input", http.StatusBadRequest)
					return
				}
				embeddings = append(embeddings, vector)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(model.Close)
	t.Setenv("BRAINDEX_OLLAMA_PORT", strconv.Itoa(model.Listener.Addr().(*net.TCPAddr).Port))
}
