package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sam-bretz/braindex/internal/connectors"
	"github.com/sam-bretz/braindex/internal/inbox"
	"github.com/sam-bretz/braindex/internal/library"
)

func TestLinkPreviewCancellationAndRouting(t *testing.T) {
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := library.Source{ID: "web-article", URL: "https://example.org/article", Title: "A public article", Status: "ready",
		Passages: []library.Passage{{Text: "Customer discovery starts with a recent example.", Locator: "extracted page line 7"}}}
	probe := func(_ context.Context, url string, _ int) (connectors.LinkPreview, error) {
		if url != source.URL {
			t.Fatalf("unexpected URL %q", url)
		}
		return connectors.LinkPreview{Kind: "article", URL: url, Sources: []library.Source{source}}, nil
	}
	run := func(input string, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := (command{root: store.Root, in: strings.NewReader(input), out: &out}).linkUsing(append([]string{source.URL}, args...), probe); err != nil {
			t.Fatalf("link: %v (%s)", err, out.String())
		}
		return out.String()
	}
	var preview linkReview
	if err := json.Unmarshal([]byte(run("", "--json")), &preview); err != nil ||
		preview.Kind != "article" || len(preview.Items) != 1 || preview.Items[0].ID != source.ID {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	run("1\n\n")
	snapshot, err := store.Snapshot()
	if err != nil || len(snapshot.Sources) != 0 || len(snapshot.Brains) != 0 {
		t.Fatalf("preview or cancellation changed library: %+v, %v", snapshot, err)
	}
	run("1\ninbox\n")
	items, err := inbox.List(store)
	if err != nil || len(items) != 1 || items[0].Original != source.URL {
		t.Fatalf("staged item = %+v, %v", items, err)
	}
	run("1\nnew:sales\n")
	var out, stderr bytes.Buffer
	code := Run([]string{"--library", store.Root, "context", "customer discovery recent example", "--brain", "sales", "--json"},
		nil, &out, &stderr)
	if code != 0 || !strings.Contains(out.String(), source.URL) || !strings.Contains(out.String(), source.Passages[0].Locator) {
		t.Fatalf("search = %d %s %s", code, out.String(), stderr.String())
	}
	run("", "--brain", "sales", "--select", source.ID)
	snapshot, err = store.Snapshot()
	if err != nil || len(snapshot.Sources) != 1 || len(snapshot.Brains) != 1 || len(snapshot.Brains[0].Sources) != 1 {
		t.Fatalf("retry duplicated library content: %+v, %v", snapshot, err)
	}
}

func TestLinkSingleVideoKeepsKnownTitle(t *testing.T) {
	root := t.TempDir()
	run := func(code int, args ...string) []byte { return followTestCommand(t, root, code, args...) }
	run(0, "init")
	run(0, "brain", "create", "known")
	run(0, "brain", "create", "new")
	const url = "https://youtu.be/Fixture001A"
	var preview linkReview
	if err := json.Unmarshal(run(0, "link", url, "--json"), &preview); err != nil || len(preview.Items) != 1 {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	store, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	id := preview.Items[0].ID
	if _, err := store.PutDiscoveredSource(library.Source{ID: id, Title: "Known title", URL: url}, []string{"known"}, true); err != nil {
		t.Fatal(err)
	}
	run(0, "link", url, "--brain", "new", "--select", id)
	source, err := store.Source(id)
	if err != nil || source.Title != "Known title" {
		t.Fatalf("saved source = %+v, %v", source, err)
	}
}
