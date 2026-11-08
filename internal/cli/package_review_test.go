package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sam-bretz/braindex/internal/brainpackage"
	"github.com/sam-bretz/braindex/internal/library"
)

func runPackage(t *testing.T, root string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(append([]string{"--library", root, "package"}, args...), nil, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func TestPackagePreviewDoesNotOpenLibrary(t *testing.T) {
	exporter := newPackageLibrary(t)
	packageDir := exportPackageFixture(t, exporter, "1.0.0", "synthetic evidence")
	root := filepath.Join(t.TempDir(), "unused")
	out, stderr, code := runPackage(t, root, "preview", packageDir, "--json")
	if code != 0 {
		t.Fatal(stderr)
	}
	var preview brainpackage.Preview
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Searchable != 1 || preview.References != 1 || preview.PublisherVerification != "unverified" || len(preview.Digest) != 64 {
		t.Fatalf("preview: %+v", preview)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("preview touched library: %v", err)
	}
	out, stderr, code = runPackage(t, root, "preview", packageDir)
	if code != 0 || !strings.Contains(out, "1 searchable after indexing, 1 reference-only") || !strings.Contains(out, "(unverified)") {
		t.Fatalf("human preview: %s %s", out, stderr)
	}
	files, err := filepath.Glob(filepath.Join(packageDir, "sources", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("fixture assets: %v, %v", files, err)
	}
	if err := os.WriteFile(files[0], []byte(`{"passages":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runPackage(t, root, "preview", packageDir)
	if code == 0 || !strings.Contains(stderr, "checksum mismatch") {
		t.Fatalf("tampered preview: %d %s", code, stderr)
	}
}

func TestPreviewPinnedGitPackage(t *testing.T) {
	exporter := newPackageLibrary(t)
	repo := exportPackageFixture(t, exporter, "1.0.0", "synthetic evidence")
	gitPackageFixture(t, repo, "init", "--quiet")
	gitPackageFixture(t, repo, "add", ".")
	gitPackageFixture(t, repo, "commit", "--quiet", "-m", "fixture")
	gitPackageFixture(t, repo, "tag", "v1.0.0")
	root := filepath.Join(t.TempDir(), "unused")
	out, stderr, code := runPackage(t, root, "preview", repo, "--git", "--version", "1.0.0", "--json")
	if code != 0 {
		t.Fatal(stderr)
	}
	var preview brainpackage.Preview
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Commit != gitPackageFixture(t, repo, "rev-parse", "HEAD") || preview.Searchable != 1 || preview.References != 1 {
		t.Fatalf("Git preview: %+v", preview)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("preview touched library: %v", err)
	}
}

func newPackageLibrary(t *testing.T) *library.Store {
	t.Helper()
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBrain(library.Brain{ID: "sales"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSource(library.Source{URL: "https://example.org/reference"}, []string{"sales"}); err != nil {
		t.Fatal(err)
	}
	return store
}

func exportPackageFixture(t *testing.T, store *library.Store, version, body string) string {
	t.Helper()
	source, err := store.PutSource(library.Source{ID: "guide", Title: "Fixture guide", Redistribution: "CC0-1.0",
		Passages: []library.Passage{{Text: body, Locator: "line 1"}},
	}, []string{"sales"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "package")
	if _, err := brainpackage.Export(store, brainpackage.ExportOptions{BrainID: "sales", Directory: dir, Version: version, Publisher: "Fixture publisher", Include: []string{source.ID}}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func gitPackageFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.org", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPinnedGitPackageUpgradePreservesLocalCuration(t *testing.T) {
	exporter := newPackageLibrary(t)
	if _, err := exporter.PutSource(library.Source{URL: "https://example.org/obsolete"}, []string{"sales"}); err != nil {
		t.Fatal(err)
	}
	repo := exportPackageFixture(t, exporter, "1.0.0", "original appointment guidance")
	gitPackageFixture(t, repo, "init", "--quiet")
	gitPackageFixture(t, repo, "add", ".")
	gitPackageFixture(t, repo, "commit", "--quiet", "-m", "first package")
	gitPackageFixture(t, repo, "tag", "v1.0.0")
	next := exportPackageFixture(t, exporter, "2.0.0", "revised appointment guidance")
	manifest, err := brainpackage.Validate(next)
	if err != nil {
		t.Fatal(err)
	}
	var entries []brainpackage.Entry
	for _, entry := range manifest.Sources {
		if entry.URL == "https://example.org/obsolete" {
			continue
		}
		if entry.URL == "https://example.org/reference" {
			entry.URL = "https://example.org/revised-reference"
		}
		entries = append(entries, entry)
	}
	manifest.Sources = append(entries, brainpackage.Entry{ID: "new-reference", URL: "https://example.org/new"})
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "brain.json"), manifestData, 0600); err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Sources {
		if entry.File == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(next, entry.File))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, entry.File), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitPackageFixture(t, repo, "add", ".")
	gitPackageFixture(t, repo, "commit", "--quiet", "-m", "second package")
	gitPackageFixture(t, repo, "tag", "v2.0.0")

	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		if code := Run(append([]string{"--library", store.Root}, args...), nil, &out, &stderr); code != 0 {
			t.Fatalf("%v: %s", args, stderr.String())
		}
		return out.String()
	}
	run("package", "install", repo, "--git", "--version", "1.0.0", "--brain", "installed")
	// Simulate a receipt written before package-member tracking was introduced.
	libraryPath := filepath.Join(store.MetaDir(), "library.json")
	oldData, err := os.ReadFile(libraryPath)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(oldData, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "package_members")
	oldData, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(libraryPath, oldData, 0600); err != nil {
		t.Fatal(err)
	}
	local, err := store.PutSource(library.Source{Title: "User notes", Passages: []library.Passage{{Text: "local appointment rubric", Locator: "line 1"}}}, []string{"installed"})
	if err != nil {
		t.Fatal(err)
	}
	run("rule", "set", "appointments", "--brain", "installed", "--match", "appointment")
	run("rule", "access", "installed", "deny")
	compare := func() brainpackage.Comparison {
		t.Helper()
		var report brainpackage.Comparison
		out := run("package", "compare", "installed", repo, "--git", "--version", "2.0.0", "--json")
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatal(err)
		}
		return report
	}
	beforeCompare, err := os.ReadFile(libraryPath)
	if err != nil {
		t.Fatal(err)
	}
	report := compare()
	afterCompare, err := os.ReadFile(libraryPath)
	if err != nil || !bytes.Equal(beforeCompare, afterCompare) {
		t.Fatalf("comparison modified the library: %v", err)
	}
	if len(report.LocalSources) != 1 || report.LocalSources[0] != local.ID || len(report.Conflicts) != 0 || len(report.Changes) != 4 {
		t.Fatalf("comparison: %+v", report)
	}
	var add, remove, changedBundle, changedReference bool
	for _, change := range report.Changes {
		switch change.Change {
		case "added":
			add = change.ID == "new-reference" && !change.AfterSearchable
		case "removed":
			remove = !change.BeforeSearchable
		case "changed":
			if change.BeforeSearchable && change.AfterSearchable {
				changedBundle = true
			} else if !change.BeforeSearchable && !change.AfterSearchable {
				changedReference = true
			}
		}
	}
	if !add || !remove || !changedBundle || !changedReference {
		t.Fatalf("changes: %+v", report.Changes)
	}
	upgrade := []string{"upgrade", "installed", repo, "--git", "--version", "2.0.0", "--confirm"}
	_, stderr, code := runPackage(t, store.Root, append(upgrade, "wrong-digest")...)
	if code == 0 || !strings.Contains(stderr, "confirmation") {
		t.Fatalf("wrong confirmation: %d %s", code, stderr)
	}
	installedSources, err := store.Sources()
	if err != nil {
		t.Fatal(err)
	}
	var guide library.Source
	for _, source := range installedSources {
		if source.Title == "Fixture guide" {
			guide = source
			break
		}
	}
	if guide.ID == "" {
		t.Fatal("missing package source")
	}
	edited := guide
	edited.Passages = []library.Passage{{Text: "unmerged local package edits", Locator: "line 1"}}
	if _, err := store.PutSource(edited, []string{"installed"}); err != nil {
		t.Fatal(err)
	}
	if conflict := compare(); len(conflict.Conflicts) != 1 {
		t.Fatalf("expected conflict: %+v", conflict)
	}
	_, stderr, code = runPackage(t, store.Root, append(upgrade, report.Candidate.Digest)...)
	if code == 0 || !strings.Contains(stderr, "local changes") {
		t.Fatalf("conflict did not stop upgrade: %d %s", code, stderr)
	}
	if receipt, err := store.InstalledPackage("installed"); err != nil || receipt.Version != "1.0.0" {
		t.Fatalf("failed upgrade changed receipt: %+v %v", receipt, err)
	}
	if _, err := store.PutSource(guide, []string{"installed"}); err != nil {
		t.Fatal(err)
	}
	upgrade = append(upgrade, report.Candidate.Digest, "--json")
	for range 2 {
		out := run(append([]string{"package"}, upgrade...)...)
		if !strings.Contains(out, `"version": "2.0.0"`) || !strings.Contains(out, report.Candidate.Commit) {
			t.Fatalf("receipt: %s", out)
		}
	}
	run("index", "rebuild")
	if out := run("context", "appointment", "--mode", "keyword"); !strings.Contains(out, "denied") {
		t.Fatalf("denied access was lost: %s", out)
	}
	run("rule", "access", "installed", "allow")
	if out := run("context", "appointment", "--mode", "keyword"); !strings.Contains(out, "revised appointment guidance") ||
		!strings.Contains(out, "local appointment rubric") || !strings.Contains(out, "line 1") || strings.Contains(out, "original appointment guidance") {
		t.Fatalf("updated routed retrieval: %s", out)
	}
}
