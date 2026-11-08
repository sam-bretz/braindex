package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestInitPreservesExistingFiles(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(existing, []byte("existing library"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open before init = %v", err)
	}
	store := mustInit(t, root)
	if err := store.CreateBrain(Brain{ID: "../escape"}); err == nil {
		t.Fatal("accepted an invalid brain ID")
	}
	if err := store.CreateBrain(Brain{ID: "sales", Kind: "person"}); err != nil {
		t.Fatal(err)
	}
	store = mustInit(t, root)
	brains, err := store.Brains()
	if err != nil || len(brains) != 1 || brains[0].Name != "sales" {
		t.Fatalf("brains = %+v, %v", brains, err)
	}
	got, err := os.ReadFile(existing)
	if err != nil || string(got) != "existing library" {
		t.Fatalf("existing file = %q, %v", got, err)
	}
}

func TestPutSourceRevisionsAndMemberships(t *testing.T) {
	root := t.TempDir()
	store := mustInit(t, root)
	for _, id := range []string{"sales", "work"} {
		if err := store.CreateBrain(Brain{ID: id}); err != nil {
			t.Fatal(err)
		}
	}

	draft := Source{Path: filepath.Join(root, "guide.txt"), Passages: []Passage{
		{Text: "Use customer language.", Locator: "lines 1-2"},
	}}
	first, err := store.PutSource(draft, []string{"sales"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Path != "guide.txt" || first.Status != "ready" {
		t.Fatalf("first source = %+v", first)
	}
	same, err := store.PutSource(draft, []string{"sales", "work"})
	if err != nil || !reflect.DeepEqual(first, same) {
		t.Fatalf("reimport = %+v, %v", same, err)
	}
	copy := draft
	copy.Path = filepath.Join(root, "duplicate.txt")
	for range 2 {
		duplicate, err := store.PutSource(copy, []string{"work"})
		if err != nil || !reflect.DeepEqual(first, duplicate) {
			t.Fatalf("import copy = %+v, %v", duplicate, err)
		}
	}
	draft.Passages = []Passage{{Text: "Ask for a concrete example.", Locator: "line 1"}}
	second, err := store.PutSource(draft, []string{"sales"})
	if err != nil || second.ID != first.ID || second.Revision == first.Revision {
		t.Fatalf("changed import = %+v, %v", second, err)
	}
	old, err := store.SourceRevision(first.ID, first.Revision)
	if err != nil || !reflect.DeepEqual(old, first) {
		t.Fatalf("old revision = %+v, %v", old, err)
	}
	got, err := store.Snapshot()
	if err != nil || len(got.Sources) != 1 || !reflect.DeepEqual(got.Sources[0], second) {
		t.Fatalf("snapshot = %+v, %v", got, err)
	}
	for _, brain := range got.Brains {
		if !reflect.DeepEqual(brain.Sources, []string{first.ID}) {
			t.Fatalf("memberships = %+v", brain)
		}
	}
}

func TestPutSourceMissingBrainPublishesNothing(t *testing.T) {
	store := mustInit(t, t.TempDir())
	_, err := store.PutSource(Source{Title: "Synthetic", Passages: []Passage{
		{Text: "Synthetic content.", Locator: "line 1"},
	}}, []string{"missing"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("PutSource = %v", err)
	}
	sources, err := store.Sources()
	if err != nil || len(sources) != 0 {
		t.Fatalf("sources = %+v, %v", sources, err)
	}
}

func TestConcurrentBrainWrites(t *testing.T) {
	store := mustInit(t, t.TempDir())
	var writers sync.WaitGroup
	const count = 12
	for i := range count {
		writers.Go(func() {
			if err := store.CreateBrain(Brain{ID: fmt.Sprintf("brain-%d", i)}); err != nil {
				t.Error(err)
			}
		})
	}
	writers.Wait()
	brains, err := store.Brains()
	if err != nil || len(brains) != count {
		t.Fatalf("brains = %+v, %v", brains, err)
	}
}

func TestExplicitSourceIDsKeepSeparateRevisionsForSharedURL(t *testing.T) {
	store := mustInit(t, t.TempDir())
	const url = "https://example.org/article"
	for _, id := range []string{"feed-entry", "web-article"} {
		got, err := store.PutSource(Source{
			ID: id, URL: url, Passages: []Passage{{Text: id, Locator: "line 1"}},
		}, nil)
		if err != nil || got.ID != id {
			t.Fatalf("put %s = %+v, %v", id, got, err)
		}
	}
	got, err := store.PutSource(Source{URL: url, Passages: []Passage{{Text: "implicit import", Locator: "line 1"}}}, nil)
	if err != nil || got.ID != "feed-entry" {
		t.Fatalf("implicit origin = %+v, %v", got, err)
	}
	web, err := store.Source("web-article")
	if err != nil || web.Passages[0].Text != "web-article" {
		t.Fatalf("web article = %+v, %v", web, err)
	}
}

func TestPutReferenceLinksWithoutReplacingSearchableRevision(t *testing.T) {
	store := mustInit(t, t.TempDir())
	for _, id := range []string{"existing", "inbox"} {
		if err := store.CreateBrain(Brain{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	const url = "https://example.org/source"
	original, err := store.PutSource(Source{
		ID: "web-source", URL: url, Title: "A complete source",
		Passages: []Passage{{Text: "Original text.", Locator: "line 1"}},
	}, []string{"existing"})
	if err != nil {
		t.Fatal(err)
	}
	reference, err := store.PutReference(Source{URL: url}, []string{"inbox"})
	if err != nil || !reflect.DeepEqual(reference, original) {
		t.Fatalf("linked source = %+v, %v", reference, err)
	}
	current, err := store.Source(original.ID)
	if err != nil || !reflect.DeepEqual(current, original) {
		t.Fatalf("current source = %+v, %v", current, err)
	}
	brains, err := store.Brains()
	if err != nil || !reflect.DeepEqual(brains[1].Sources, []string{original.ID}) {
		t.Fatalf("brains = %+v, %v", brains, err)
	}
	const unknown = "https://example.org/new"
	placeholder, err := store.PutReference(Source{URL: unknown}, []string{"inbox"})
	if err != nil || placeholder.URL != unknown || placeholder.Status != "unavailable" || len(placeholder.Passages) != 0 {
		t.Fatalf("new reference = %+v, %v", placeholder, err)
	}
}

func mustInit(t *testing.T, root string) *Store {
	t.Helper()
	store, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
