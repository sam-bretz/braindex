package hook

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
	"github.com/sam-bretz/braindex/internal/retrieval"
)

func TestPromptHookOnlyEmitsCitedContextForMatches(t *testing.T) {
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sales", "denied"} {
		if err := store.CreateBrain(library.Brain{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.PutSource(library.Source{Title: id, Passages: []library.Passage{
			{Text: id + " founder discovery", Locator: "page 4"},
		}}, []string{id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := retrieval.SaveRule(store, retrieval.Rule{ID: "sales", Brains: []string{"sales", "denied"}, Phrases: []string{"founder discovery"}}); err != nil {
		t.Fatal(err)
	}
	if err := retrieval.SetBrainAccess(store, "denied", false); err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild(store); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Run(store, strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"unrelated"}`), &buf); err != nil || buf.Len() != 0 {
		t.Fatalf("unrelated prompt produced output %q or error %v", buf.String(), err)
	}
	if err := Run(store, strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"Founder-discovery?"}`), &buf); err != nil {
		t.Fatal(err)
	}
	var output eventOutput
	if err := json.Unmarshal(buf.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.HookSpecificOutput.Name != eventName {
		t.Fatalf("incorrect output event: %#v", output)
	}
	_, evidence, ok := strings.Cut(output.HookSpecificOutput.Context, "\n")
	if !ok {
		t.Fatalf("missing cited JSON in output: %q", output.HookSpecificOutput.Context)
	}
	var result retrieval.Result
	if err := json.Unmarshal([]byte(evidence), &result); err != nil {
		t.Fatal(err)
	}
	want, err := retrieval.Context(store, retrieval.Request{Query: "Founder-discovery?", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, result) || strings.Contains(evidence, "denied founder discovery") {
		t.Fatalf("hook result differs from scoped retrieval: got %#v; want %#v", result, want)
	}
}
