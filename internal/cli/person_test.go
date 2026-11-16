package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sam-bretz/braindex/internal/library"
)

func TestDiscoverPersonReviewsBeforeSaving(t *testing.T) {
	root := t.TempDir()
	store, err := library.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sales", "other"} {
		if err := store.CreateBrain(library.Brain{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/article" {
			fmt.Fprint(w, "<title>A real article</title><p>Measure customer discovery.</p>")
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	candidates := fmt.Sprintf(`[
		{"kind":"article","title":"A real article","url":%q},
		{"kind":"article","title":"Same article","url":%q},
		{"kind":"podcast","title":"An interview","url":"https://example.org/episode"},
		{"kind":"book","title":"Unselected book","url":"https://example.org/book"},
		{"kind":"article","title":"Unavailable article","url":%q},
		{"kind":"article","title":"Invalid URL","url":"https://user:pass@example.org"}
	]`, server.URL+"/article", server.URL+"/article#section", server.URL+"/missing")
	file := filepath.Join(t.TempDir(), "results.json")
	if err := os.WriteFile(file, []byte(candidates), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(input string, flags ...string) (int, string) {
		t.Helper()
		var out, stderr bytes.Buffer
		args := []string{"--library", root, "discover", "person", "Sample Author", "--brain", "sales", "--results", file}
		code := Run(append(args, flags...), strings.NewReader(input), &out, &stderr)
		return code, out.String() + stderr.String()
	}
	if code, out := run("", "--json"); code != 0 || strings.Contains(out, "Same article") || strings.Contains(out, "Invalid URL") {
		t.Fatalf("preview = %d %s", code, out)
	}
	if code, out := run("1,7\n"); code == 0 || !strings.Contains(out, "1 to 4") {
		t.Fatalf("invalid selection = %d %s", code, out)
	}
	if code, out := run("\n"); code != 0 {
		t.Fatalf("skip = %d %s", code, out)
	}
	if sources, err := store.Sources(); err != nil || len(sources) != 0 {
		t.Fatalf("preview/skip wrote sources: %v, %v", sources, err)
	}
	if code, out := run("1,2,4,2\n"); code != 0 || !strings.Contains(out, "Saved 2 to sales") {
		t.Fatalf("interactive selection = %d %s", code, out)
	}
	if code, out := run("", "--select", "1,4", "--import-articles"); code != 0 || !strings.Contains(out, "Article text unavailable") {
		t.Fatalf("article import = %d %s", code, out)
	}
	if requests.Load() != 0 {
		t.Fatal("person import contacted a private article URL")
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, source := range snapshot.Sources {
		status[source.URL] = source.Status
	}
	if len(status) != 3 || status[server.URL+"/article"] != "unavailable" || status[server.URL+"/missing"] != "unavailable" ||
		status["https://example.org/episode"] != "unavailable" {
		t.Fatalf("sources = %v", status)
	}
	for _, brain := range snapshot.Brains {
		if (brain.ID == "sales" && len(brain.Sources) != 3) || (brain.ID == "other" && len(brain.Sources) != 0) {
			t.Fatalf("unexpected brain membership: %+v", brain)
		}
	}
}
