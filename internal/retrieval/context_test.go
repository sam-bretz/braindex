package retrieval

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
)

func TestContextUsesUserRulesAndSeparatesOutcomes(t *testing.T) {
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sales", "denied", "missing", "elsewhere"} {
		if err := store.CreateBrain(library.Brain{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	add := func(brain, title, text string) library.Source {
		t.Helper()
		source := library.Source{ID: brain + "-source", Title: title, Author: "Test Author"}
		if text != "" {
			source.Passages = []library.Passage{{Text: text, Locator: "page 7"}}
		}
		saved, err := store.PutSource(source, []string{brain})
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	sales := add("sales", "Test title", strings.Repeat("前", 1000)+" customer discovery "+strings.Repeat("後", 800))
	add("denied", "Denied title", "customer discovery TOP SECRET")
	add("missing", "Missing text", "")
	add("elsewhere", "Different topic", "obscure discovery customer")
	if err := index.Rebuild(store); err != nil {
		t.Fatal(err)
	}
	for _, rule := range []Rule{
		{ID: "customer", Phrases: []string{"customer"}, Brains: []string{"sales", "denied"}},
		{ID: "discovery", Phrases: []string{"customer discovery"}, Brains: []string{"sales", "missing"}},
	} {
		if err := SaveRule(store, rule); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetBrainAccess(store, "denied", false); err != nil {
		t.Fatal(err)
	}
	retrieve := func(req Request) Result {
		t.Helper()
		result, err := Context(store, req)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	result := retrieve(Request{Query: "Customer-discovery!"})
	if result.Status != "matched" || result.Routing.Scope != "matched_rules" ||
		!reflect.DeepEqual(result.Topics, []string{"customer", "discovery"}) ||
		!reflect.DeepEqual(result.Brains[0].SelectedByTopics, result.Topics) {
		t.Fatalf("unexpected routing result: %#v", result)
	}
	if len(result.Brains) != 3 ||
		result.Brains[0].ID != "sales" || result.Brains[0].Status != "hits" ||
		result.Brains[1].ID != "denied" || result.Brains[1].Status != "denied" || len(result.Brains[1].Excerpts) != 0 ||
		result.Brains[2].ID != "missing" || result.Brains[2].Status != "unavailable" ||
		!reflect.DeepEqual(result.Brains[2].UnavailableSources, []string{"missing-source"}) {
		t.Fatalf("unexpected brain results: %#v", result.Brains)
	}
	payload, err := json.Marshal(result)
	if err != nil || strings.Contains(string(payload), "TOP SECRET") || strings.Contains(string(payload), "denied-source") ||
		result.Brains[1].Searched || result.Brains[2].Searched || !result.Brains[0].Searched {
		t.Fatalf("unexpected search or privacy outcome: %s, %v", payload, err)
	}
	if len(result.Brains[0].Excerpts) != 1 {
		t.Fatalf("unexpected excerpts: %#v", result.Brains[0].Excerpts)
	}
	excerpt := result.Brains[0].Excerpts[0]
	if excerpt.SourceID != sales.ID || excerpt.Revision != sales.Revision || excerpt.Locator != "page 7" ||
		excerpt.Title != sales.Title || excerpt.Author != sales.Author || !excerpt.Truncated ||
		utf8.RuneCountInString(excerpt.Text) > 600 || !strings.Contains(excerpt.Text, "customer discovery") {
		t.Fatalf("unexpected cited excerpt: %#v", excerpt)
	}

	empty := retrieve(Request{Query: "obscure", Topic: "customer"})
	if empty.Routing.Scope != "explicit_topic" || !empty.Brains[0].Searched || empty.Brains[0].Status != "no_relevant_passages" {
		t.Fatalf("must not search other brains after an empty result: %#v", empty)
	}
	unmatched := retrieve(Request{Query: "customers"})
	if unmatched.Status != "unmatched" || unmatched.Routing.Scope != "unmatched" || len(unmatched.Brains) != 0 {
		t.Fatalf("must not match a phrase inside another word: %#v", unmatched)
	}
	explicit := retrieve(Request{Query: "obscure", Topic: "discovery", Brains: []string{"elsewhere", "sales"}})
	if explicit.Routing.Scope != "explicit_topic" || len(explicit.Brains) != 3 ||
		!explicit.Brains[0].Explicit || len(explicit.Brains[0].SelectedByTopics) != 0 ||
		!explicit.Brains[1].Explicit || !reflect.DeepEqual(explicit.Brains[1].SelectedByTopics, []string{"discovery"}) {
		t.Fatalf("unexpected explicit routing reasons: %#v", explicit)
	}
	all := retrieve(Request{Query: "customer discovery", AllBrains: true})
	if all.Routing.Scope != "all_brains" || len(all.Brains) != 4 || len(all.Brains[3].SelectedByTopics) != 0 {
		t.Fatalf("all-brains scope should not attribute selection to a rule: %#v", all)
	}
	if err := SaveRule(store, Rule{ID: "customer", Brains: []string{"elsewhere"}}); err != nil {
		t.Fatal(err)
	}
	changed := retrieve(Request{Query: "obscure", Topic: "customer"})
	if len(changed.Brains) != 1 || changed.Brains[0].ID != "elsewhere" || changed.Brains[0].Status != "hits" {
		t.Fatalf("rule edit should affect retrieval without reindexing: %#v", changed)
	}
}
