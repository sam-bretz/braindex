package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sam-bretz/braindex/internal/inbox"
	"github.com/sam-bretz/braindex/internal/library"
)

func TestInboxReviewAndPromotion(t *testing.T) {
	root := t.TempDir()
	store, err := library.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBrain(library.Brain{ID: "sales"}); err != nil {
		t.Fatal(err)
	}
	run := func(input string, args ...string) (int, string) {
		t.Helper()
		var out, stderr bytes.Buffer
		code := Run(append([]string{"--library", root}, args...), strings.NewReader(input), &out, &stderr)
		return code, out.String() + stderr.String()
	}
	code, out := run("", "capture", "note", "Ask about the most recent real sales attempt.")
	if code != 0 {
		t.Fatal(out)
	}
	var item inbox.Item
	if err := json.Unmarshal([]byte(out), &item); err != nil {
		t.Fatal(err)
	}
	if code, _ := run(`{"digest":"invalid"}`, "inbox", "propose", item.ID); code == 0 {
		t.Fatal("accepted an incomplete proposal")
	}
	if code, out := run(`{"digest":"Recent sales attempts","rationale":"Useful in discovery","brains":["sales"]}`, "inbox", "propose", item.ID); code != 0 {
		t.Fatal(out)
	}
	if sources, err := store.Sources(); err != nil || len(sources) != 0 {
		t.Fatalf("inbox material leaked into source records: %+v, %v", sources, err)
	}
	exportDir := filepath.Join(t.TempDir(), "brain")
	if code, out := run("", "package", "export", "sales", exportDir, "--version", "1.0.0"); code != 0 || strings.Contains(out, "Recent sales attempts") {
		t.Fatalf("export: %d %s", code, out)
	}
	if code, out := run("", "inbox", "defer", item.ID); code != 0 || !strings.Contains(out, "deferred") {
		t.Fatalf("defer: %d %s", code, out)
	}
	calls := 0
	for range 2 {
		approved, err := inbox.Approve(store, item.ID, []string{"sales"}, func(original inbox.Item) (string, error) {
			calls++
			content, err := os.ReadFile(inbox.ContentPath(store, original))
			if err != nil {
				return "", err
			}
			source, err := store.PutSource(library.Source{Title: "Captured note", Passages: []library.Passage{
				{Text: string(content), Locator: "line 1"},
			}}, []string{"sales"})
			return source.ID, err
		})
		if err != nil || approved.Status != "approved" || approved.SourceID == "" {
			t.Fatalf("approval: %+v, %v", approved, err)
		}
	}
	if calls != 1 {
		t.Fatalf("approval called import %d times", calls)
	}
}

func TestInstallGitPackage(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	exporter, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := exporter.CreateBrain(library.Brain{ID: "sales"}); err != nil {
		t.Fatal(err)
	}
	if _, err := exporter.PutSource(library.Source{Title: "Reference", URL: "https://example.org/source"}, []string{"sales"}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	repo := filepath.Join(t.TempDir(), "repo")
	if code := Run([]string{"--library", exporter.Root, "package", "export", "sales", repo, "--version", "1.0.0"}, nil, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.org", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "--quiet")
	git("add", "brain.json")
	git("commit", "--quiet", "-m", "fixture")
	git("tag", "v1.0.0")
	recipient, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := installGit(recipient, repo, "1.0.0", "installed")
	if err != nil || manifest.Version != "1.0.0" {
		t.Fatalf("install: %+v, %v", manifest, err)
	}
	if brains, err := recipient.Brains(); err != nil || len(brains) != 1 || brains[0].ID != "installed" {
		t.Fatalf("installed brain: %+v, %v", brains, err)
	}
	if _, err := installGit(recipient, repo, "1.1.0", "missing"); err == nil {
		t.Fatal("installed an unpinned version")
	}
}

func TestGitPackageTreeRejectsNonDataEntries(t *testing.T) {
	manifest := "100644 blob object 10\tbrain.json\x00"
	if paths, err := packageGitPaths([]byte(manifest + "100644 blob object 20\tsources/source.json\x00")); err != nil || len(paths) != 2 {
		t.Fatalf("valid data tree: %v, %v", paths, err)
	}
	for _, entry := range []string{
		"120000 blob object 20\tsources/source.json\x00",
		"100755 blob object 20\tsources/source.json\x00",
		"100644 blob object 16777217\tsources/source.json\x00",
		"100644 blob object 20\tsources/sub/source.json\x00",
	} {
		if _, err := packageGitPaths([]byte(manifest + entry)); err == nil {
			t.Fatal("accepted an invalid Git package tree")
		}
	}
}
