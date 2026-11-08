package brainpackage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sam-bretz/braindex/internal/library"
)

func TestExportAndInstall(t *testing.T) {
	store := fixtureStore(t)
	public := addSource(t, store, library.Source{
		Title: "A public reference with a private title", URL: "https://example.org/article",
		Author: "Private attribution", Passages: []library.Passage{{Text: "PRIVATE EXCERPT", Locator: "line 1"}},
	})
	bundle := addSource(t, store, library.Source{
		Title: "Synthetic guide", Path: "/private/fixture.txt", Author: "Fixture author",
		Redistribution: "CC0-1.0", Passages: []library.Passage{{Text: "Ask for a concrete example.", Locator: "line 2"}},
	})
	addSource(t, store, library.Source{Title: "Private file", Path: "/private/excluded.txt"})
	addSource(t, store, library.Source{Title: "Private newsletter", URL: "https://example.org/issue?token=secret"})
	addSource(t, store, library.Source{Title: "Credentials", URL: "https://user:secret@example.org/issue"})
	addSource(t, store, library.Source{Title: "Local network", URL: "http://127.0.0.1/private"})

	dir := filepath.Join(t.TempDir(), "out")
	manifest, err := Export(store, ExportOptions{BrainID: "sales", Directory: dir, Include: []string{bundle.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Sources) != 2 {
		t.Fatalf("manifest = %+v", manifest)
	}
	data, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{public.Title, public.Author, "PRIVATE EXCERPT", "/private/", "token=", "secret", "127.0.0.1"} {
		if strings.Contains(string(data), bad) {
			t.Fatalf("manifest leaked %q: %s", bad, data)
		}
	}
	referencesOnly, err := Export(store, ExportOptions{BrainID: "sales", Directory: filepath.Join(t.TempDir(), "references")})
	if err != nil || len(referencesOnly.Sources) != 1 || referencesOnly.Sources[0].File != "" {
		t.Fatalf("references-only export = %+v, %v", referencesOnly, err)
	}
	if _, err := Export(store, ExportOptions{BrainID: "sales", Directory: filepath.Join(t.TempDir(), "denied"), Include: []string{public.ID}}); err == nil {
		t.Fatal("export accepted a private source for bundling")
	}
	if _, err := Export(store, ExportOptions{BrainID: "sales", Directory: dir}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing export destination = %v", err)
	}

	recipient, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := Install(recipient, dir, "installed-sales", "file:fixture"); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := recipient.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Brains) != 1 || len(snapshot.Sources) != 2 || len(snapshot.Brains[0].Sources) != 2 {
		t.Fatalf("installed snapshot = %+v", snapshot)
	}
	for _, source := range snapshot.Sources {
		switch source.Title {
		case "https://example.org/article":
			if source.Status != "unavailable" || len(source.Passages) != 0 {
				t.Fatalf("reference = %+v", source)
			}
		case "Synthetic guide":
			if source.Status != "ready" || len(source.Passages) != 1 || source.Passages[0].Locator != "line 2" || source.Path != "" {
				t.Fatalf("bundled source = %+v", source)
			}
		default:
			t.Fatalf("unexpected installed source = %+v", source)
		}
	}
	if _, err := Install(recipient, dir, "installed-sales", "file:other-origin"); !errors.Is(err, library.ErrExists) {
		t.Fatalf("receipt change = %v", err)
	}
}

func TestInstallRejectsInvalidPackageWithoutMutatingLibrary(t *testing.T) {
	for _, attack := range []string{"symlink", "traversal", "checksum", "instructions"} {
		t.Run(attack, func(t *testing.T) {
			store := fixtureStore(t)
			source := addSource(t, store, library.Source{
				Title: "Synthetic guide", Redistribution: "CC0-1.0",
				Passages: []library.Passage{{Text: "A verifiable quotation.", Locator: "line 1"}},
			})
			dir := filepath.Join(t.TempDir(), "out")
			manifest, err := Export(store, ExportOptions{BrainID: "sales", Directory: dir, Include: []string{source.ID}})
			if err != nil {
				t.Fatal(err)
			}
			asset := filepath.Join(dir, manifest.Sources[0].File)
			switch attack {
			case "symlink":
				outside := filepath.Join(t.TempDir(), "outside.json")
				if err := os.Rename(asset, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, asset); err != nil {
					t.Fatal(err)
				}
			case "traversal":
				manifest.Sources[0].File = "../outside.json"
				data, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filepath.Join(dir, manifestFile), data)
			case "checksum":
				writeTestFile(t, asset, []byte(`{"passages":[]}`))
			case "instructions":
				data, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				data = append(data[:len(data)-1], []byte(`,"rules":["founder-led sales"]}`)...)
				writeTestFile(t, filepath.Join(dir, manifestFile), data)
			}
			recipient, err := library.Init(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Install(recipient, dir, "installed-sales", "file:fixture"); err == nil {
				t.Fatal("installed a malformed package")
			}
			snapshot, err := recipient.Snapshot()
			if err != nil || len(snapshot.Brains) != 0 || len(snapshot.Sources) != 0 {
				t.Fatalf("malformed package mutated library: %+v, %v", snapshot, err)
			}
		})
	}
}

func fixtureStore(t *testing.T) *library.Store {
	t.Helper()
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBrain(library.Brain{ID: "sales", Name: "Founder-led sales"}); err != nil {
		t.Fatal(err)
	}
	return store
}

func addSource(t *testing.T, store *library.Store, source library.Source) library.Source {
	t.Helper()
	got, err := store.PutSource(source, []string{"sales"})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestShareURLRejectsCarrierGradeNAT(t *testing.T) {
	for _, url := range []string{"https://100.64.0.1/note", "http://[::ffff:100.127.255.255]/note"} {
		if shareURL(url) != "" {
			t.Fatalf("shared a carrier-grade NAT address: %s", url)
		}
	}
}
