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
			ID, Question string
			Brains       []string
			Expected     map[string]expectedPassage
			Vector       []int
		}
	}
	data, err := os.ReadFile("testdata/complementary-evidence.json")
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
	for _, brain := range []string{"sales", "markets", "evals", "operations", "empty"} {
		if err := store.CreateBrain(library.Brain{ID: brain}); err != nil {
			t.Fatal(err)
		}
	}
	vectors := map[string][]int{}
	for _, source := range fixture.Sources {
		_, err := store.PutSource(library.Source{ID: source.ID, Title: source.Title,
			Passages: []library.Passage{{Text: source.Text, Locator: source.Locator}}}, []string{source.Brain})
		if err != nil {
			t.Fatal(err)
		}
		vectors["search_document: "+source.Text] = source.Vector
	}
	for _, sample := range fixture.Queries {
		vectors["search_query: "+sample.Question] = sample.Vector
	}
	if err := index.Rebuild(store); err != nil {
		t.Fatal(err)
	}

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
	defer model.Close()
	t.Setenv("BRAINDEX_OLLAMA_PORT", strconv.Itoa(model.Listener.Addr().(*net.TCPAddr).Port))
	if err := index.RebuildEmbeddings(t.Context(), store, "fixture", 0.55); err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"keyword", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			requestMode := mode
			if mode == "hybrid" {
				requestMode = "auto"
			}
			var expected, top1, top3, passages, irrelevant, irrelevantBrains, contributingBrains int
			for _, sample := range fixture.Queries {
				result, err := Context(store, Request{Query: sample.Question, Brains: sample.Brains, Mode: requestMode, Limit: 3})
				if err != nil || result.Retrieval != mode {
					t.Fatalf("%s: %s retrieval failed: %v", sample.ID, result.Retrieval, err)
				}
				expected += len(sample.Expected)
				for _, brain := range result.Brains {
					want, relevant := sample.Expected[brain.ID]
					if len(brain.Excerpts) > 0 {
						contributingBrains++
						if !relevant {
							irrelevantBrains++
						}
					}
					for rank, excerpt := range brain.Excerpts {
						passages++
						if excerpt.Title == "" || excerpt.Locator == "" || excerpt.Revision == "" {
							t.Fatalf("%s: uncitable excerpt %#v", sample.ID, excerpt)
						}
						if relevant && excerpt.SourceID == want.Source && excerpt.Locator == want.Locator {
							top3++
							if rank == 0 {
								top1++
							}
						} else {
							irrelevant++
						}
					}
				}
			}
			t.Logf("per-brain top-1 coverage %d/%d, top-3 coverage %d/%d, irrelevant passages %d/%d, irrelevant contributing brains %d/%d",
				top1, expected, top3, expected, irrelevant, passages, irrelevantBrains, contributingBrains)
			if top1 != expected || top3 != expected || irrelevant != 0 || irrelevantBrains != 0 {
				t.Fatal("complementary evidence is missing or an irrelevant passage contributed")
			}
		})
	}
}
