package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
	"github.com/sam-bretz/braindex/internal/retrieval"
)

func TestContextExplainKeepsStructuredOutput(t *testing.T) {
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, brain := range []string{"sales", "private"} {
		if err := store.CreateBrain(library.Brain{ID: brain}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.PutSource(library.Source{Title: brain + " title", Passages: []library.Passage{
			{Text: brain + " budget approval", Locator: "line 1"},
		}}, []string{brain}); err != nil {
			t.Fatal(err)
		}
	}
	if err := retrieval.SaveRule(store, retrieval.Rule{ID: "budget", Phrases: []string{"budget approval"}, Brains: []string{"sales", "private"}}); err != nil {
		t.Fatal(err)
	}
	if err := retrieval.SetBrainAccess(store, "private", false); err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild(store); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	run := func(args ...string) string {
		t.Helper()
		out.Reset()
		stderr.Reset()
		if code := Run(append([]string{"--library", store.Root, "context", "budget approval"}, args...),
			strings.NewReader(""), &out, &stderr); code != 0 {
			t.Fatalf("context failed: %s", &stderr)
		}
		return out.String()
	}

	basic := run()
	if basic != run("--json") {
		t.Fatal("explicit JSON should match default output")
	}
	var result retrieval.Result
	if err := json.Unmarshal([]byte(basic), &result); err != nil || result.Routing.Scope != "matched_rules" || len(result.Brains) != 2 {
		t.Fatalf("missing JSON routing details: %s (%v)", basic, err)
	}
	text := run("--explain")
	if !strings.Contains(text, "Selected by topics: budget") ||
		!strings.Contains(text, "private: denied (searched: false)") || strings.Contains(text, "private title") ||
		!strings.Contains(text, `"sales title", "line 1" (revision`) {
		t.Fatalf("unexpected explanation: %s", text)
	}
}
