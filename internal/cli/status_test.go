package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
)

func TestStatusTracksLibraryAndIndexFreshness(t *testing.T) {
	root := t.TempDir()
	status := func() libraryHealth {
		t.Helper()
		var out, stderr bytes.Buffer
		code := Run([]string{"--library", root, "status", "--json"}, strings.NewReader(""), &out, &stderr)
		if code != 0 {
			t.Fatalf("status failed: %s", stderr.String())
		}
		if strings.Contains(out.String(), "private fixture text") {
			t.Fatal("status leaked passage content")
		}
		var got libraryHealth
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := status(); got.State != "uninitialized" || got.Next != "braindex init" {
		t.Fatalf("unexpected uninitialized status: %+v", got)
	}
	store, err := library.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBrain(library.Brain{ID: "fixture"}); err != nil {
		t.Fatal(err)
	}
	add := func(id, title string, passages []library.Passage) {
		t.Helper()
		if _, err := store.PutSource(library.Source{ID: id, Title: title, Passages: passages}, []string{"fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	add("sample", "Example", []library.Passage{{Text: "private fixture text", Locator: "line 1"}})
	add("unavailable", "Reference", nil)
	got := status()
	if got.Keyword.State != "missing" || got.Embeddings.State != "missing" || len(got.Brains) != 1 ||
		got.Brains[0].Searchable != 1 || got.Brains[0].Unavailable != 1 {
		t.Fatalf("unexpected missing-index status: %+v", got)
	}
	if err := index.Rebuild(store); err != nil {
		t.Fatal(err)
	}
	if got := status(); got.Keyword.State != "current" {
		t.Fatalf("unexpected rebuilt status: %+v", got)
	}
	add("sample", "Example", []library.Passage{{Text: "revised private fixture text", Locator: "line 1"}})
	if got := status(); got.Keyword.State != "stale" || got.Keyword.Next != "braindex index rebuild" {
		t.Fatalf("unexpected stale status: %+v", got)
	}
}
