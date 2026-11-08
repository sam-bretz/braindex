package index

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sam-bretz/braindex/internal/library"
)

func TestOfflineRebuildAndRevisionFreshness(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library with space #?")
	store, err := library.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBrain(library.Brain{ID: "sales", Name: "Sales"}); err != nil {
		t.Fatal(err)
	}
	first, err := store.PutSource(library.Source{
		ID: "source", Title: "Source", Passages: []library.Passage{
			{Text: "Customer discovery questions", Locator: "line 1"},
			{Text: "Customer discovery process", Locator: "line 2"},
		},
	}, []string{"sales"})
	if err != nil {
		t.Fatal(err)
	}
	search := func(store *library.Store) []Match {
		t.Helper()
		sources, err := store.Sources()
		if err != nil {
			t.Fatal(err)
		}
		idx, err := Open(store, sources)
		if err != nil {
			t.Fatal(err)
		}
		defer idx.Close()
		matches, err := idx.Search(`"discovery"* OR (unterminated`, []string{first.ID}, 10)
		if err != nil {
			t.Fatal(err)
		}
		return matches
	}

	if _, err := Open(store, []library.Source{first}); !errors.Is(err, ErrStale) {
		t.Fatalf("missing index should be stale: %v", err)
	}
	if err := Rebuild(store); err != nil {
		t.Fatal(err)
	}
	before := search(store)
	if len(before) != 2 || before[0].Revision != first.Revision {
		t.Fatalf("unexpected results: %#v", before)
	}
	restoreRoot := t.TempDir()
	restoreMeta := filepath.Join(restoreRoot, ".braindex")
	if err := os.Mkdir(restoreMeta, 0700); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(store.MetaDir(), "library.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(restoreMeta, "library.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(restoreMeta, "sources"), os.DirFS(filepath.Join(store.MetaDir(), "sources"))); err != nil {
		t.Fatal(err)
	}
	restored, err := library.Open(restoreRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := Rebuild(restored); err != nil {
		t.Fatal(err)
	}
	if after := search(restored); !reflect.DeepEqual(before, after) {
		t.Fatalf("results changed after restoring and rebuilding: before %#v, after %#v", before, after)
	}

	second, err := store.PutSource(library.Source{
		ID: first.ID, Title: first.Title, Passages: []library.Passage{
			{Text: "Customer discovery interviews", Locator: "line 1"},
		},
	}, []string{"sales"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(store, []library.Source{second}); !errors.Is(err, ErrStale) {
		t.Fatalf("index should be stale after updating a source: %v", err)
	}
	if err := Rebuild(store); err != nil {
		t.Fatal(err)
	}
	if after := search(store); len(after) != 1 || after[0].Revision != second.Revision {
		t.Fatalf("unexpected rebuilt results: %#v", after)
	}
	if _, err := store.SourceRevision(first.ID, first.Revision); err != nil {
		t.Fatalf("original citation should still resolve: %v", err)
	}
}
