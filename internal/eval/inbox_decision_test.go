package eval

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/sam-bretz/braindex/internal/cli"
	"github.com/sam-bretz/braindex/internal/inbox"
	"github.com/sam-bretz/braindex/internal/library"
	"github.com/sam-bretz/braindex/internal/retrieval"
)

func TestLocalInboxSuggestions(t *testing.T) {
	model := os.Getenv("BRAINDEX_EVAL_MODEL")
	if model == "" {
		t.Skip("set BRAINDEX_EVAL_MODEL to an installed loopback embedding model")
	}
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		id, name, title, example string
	}{
		{"sales", "Enterprise sales discovery and buyer qualification", "Interviewing buyers", "Ask about purchase authority, current workarounds, and unmet needs in prospect interviews."},
		{"evals", "AI model evaluation and regression testing", "Testing generated answers", "Score model responses on gold datasets with labels for citation correctness and hallucinations."},
		{"private", "Flower care and planting bulbs", "Garden plans", "Plan planting and watering schedules for tulips and daffodils."},
		{"wood-a", "Woodworking joints and cabinet assembly", "Cutting joinery", "Cut matching mortise and tenon joints for wooden cabinet frames."},
		{"wood-b", "Woodworking joints and cabinet assembly", "Cutting joinery", "Cut matching mortise and tenon joints for wooden cabinet frames."},
	} {
		if err := store.CreateBrain(library.Brain{ID: sample.id, Name: sample.name}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.PutSource(library.Source{
			ID: sample.id + "-example", Title: sample.title,
			Passages: []library.Passage{{Text: sample.example, Locator: "line 1"}},
		}, []string{sample.id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := retrieval.SetBrainAccess(store, "private", false); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, kind, text, wantBrain, wantReason string
	}{
		{"buyer", "note", "For the prospect discovery call, ask how buyers currently solve the problem and who controls purchasing.", "sales", "best_match"},
		{"regressions", "note", "Build a labeled regression benchmark for an LLM. Check generated answers against reference citations.", "evals", "best_match"},
		{"off-topic", "note", "Feed the sourdough starter with rye flour before baking the loaf.", "", "weak_match"},
		{"denied-only", "note", "When should I plant tulip and daffodil bulbs and water the flowers?", "", "weak_match"},
		{"ambiguous", "note", "I need to cut matching mortise and tenon joints for the cabinet frame.", "", "ambiguous_match"},
		{"url", "url", "https://example.org/planting-tulips", "", "unsupported_kind"},
	}
	args := []string{"--library", store.Root, "inbox", "suggest", "--model", model}
	selected := make([]string, 0, len(cases))
	before := make(map[string]inbox.Item, len(cases)+1)
	for _, sample := range cases {
		item, err := inbox.Capture(store, sample.kind, sample.text)
		if err != nil {
			t.Fatal(err)
		}
		before[item.ID] = item
		selected = append(selected, item.ID)
	}
	unselected, err := inbox.Capture(store, "note", "Unselected item about managing a sailboat.")
	if err != nil {
		t.Fatal(err)
	}
	before[unselected.ID] = unselected
	catalog, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	var out, stderr bytes.Buffer
	start := time.Now()
	if code := cli.Run(append(args, selected...), nil, &out, &stderr); code != 0 {
		t.Fatalf("inbox suggest exit %d: %s", code, stderr.String())
	}

	var result struct {
		Suggestions []struct {
			ID     string  `json:"item_id"`
			Status string  `json:"status"`
			Brain  string  `json:"brain_id"`
			Reason string  `json:"reason"`
			Score  float64 `json:"score"`
		} `json:"suggestions"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Suggestions) != len(cases) {
		t.Fatalf("expected exactly %d suggestions: %s", len(cases), out.String())
	}
	for i, item := range result.Suggestions {
		sample := cases[i]
		wantStatus := "abstained"
		if sample.wantBrain != "" {
			wantStatus = "suggested"
		}
		if item.ID != selected[i] || item.Brain != sample.wantBrain || item.Status != wantStatus || item.Reason != sample.wantReason {
			t.Errorf("%s: wrong suggestion: %+v", sample.name, item)
		}
		t.Logf("%s: %s %s (%s, score %.3f)", sample.name, item.Status, item.Brain, item.Reason, item.Score)
	}

	for id, item := range before {
		after, err := inbox.Get(store, id)
		if err != nil || !reflect.DeepEqual(item, after) {
			t.Fatalf("suggest changed inbox item %s: %v", id, err)
		}
	}
	after, err := store.Snapshot()
	if err != nil || !reflect.DeepEqual(catalog, after) {
		t.Fatalf("suggest changed the library: %v", err)
	}
	t.Logf("loopback inbox %s: %d selected items in %s", model, len(cases), time.Since(start).Round(time.Millisecond))
}
