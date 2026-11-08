package catalog

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sam-bretz/braindex/internal/brainpackage"
	"github.com/sam-bretz/braindex/internal/library"
)

func TestCatalogListsAndInstallsExactVersion(t *testing.T) {
	data := zipFixture(t, "1.0.0")
	var requested atomic.Int32
	server := fixtureServer(t, data, checksum(data), &requested)
	defer server.Close()
	entries, err := List(t.Context(), server.URL+"/")
	if err != nil || len(entries) != 1 || entries[0].Publisher != "Fixture publisher" || entries[0].Version != "1.0.0" {
		t.Fatalf("List = %+v, %v", entries, err)
	}
	store := fixtureLibrary(t)
	if _, err := Install(t.Context(), store, server.URL, "sales", "2.0.0", ""); err == nil || requested.Load() != 0 {
		t.Fatalf("wrong version attempted download: %v, %d downloads", err, requested.Load())
	}
	got, err := Install(t.Context(), store, server.URL, "sales", "1.0.0", "")
	if err != nil || got.PackageID != "sales" || requested.Load() != 1 {
		t.Fatalf("Install = %+v, %v, %d downloads", got, err, requested.Load())
	}
	snapshot, err := store.Snapshot()
	if err != nil || len(snapshot.Brains) != 1 || len(snapshot.Sources) != 1 || snapshot.Sources[0].Status != "ready" {
		t.Fatalf("installed library = %+v, %v", snapshot, err)
	}
}

func TestCatalogRejectsWrongDigestAndPackageIdentity(t *testing.T) {
	data := zipFixture(t, "1.0.0")
	for _, name := range []string{"checksum", "identity"} {
		t.Run(name, func(t *testing.T) {
			check := checksum(data)
			if name == "checksum" {
				check = checksum([]byte("wrong content"))
			} else {
				data = zipFixture(t, "2.0.0")
				check = checksum(data)
			}
			var requested atomic.Int32
			server := fixtureServer(t, data, check, &requested)
			defer server.Close()
			store := fixtureLibrary(t)
			if _, err := Install(t.Context(), store, server.URL, "sales", "1.0.0", ""); err == nil {
				t.Fatal("invalid package installed")
			}
			brains, err := store.Brains()
			if err != nil || len(brains) != 0 {
				t.Fatalf("brains = %+v, %v", brains, err)
			}
		})
	}
}

func TestCatalogRejectsCrossOriginRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("cross-origin request reached target")
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	if _, err := List(context.Background(), server.URL); err == nil {
		t.Fatal("cross-origin redirect accepted")
	}
}

func TestUnpackRejectsTraversalAndSymlink(t *testing.T) {
	for _, name := range []string{"../outside", "sources/source.json"} {
		t.Run(name, func(t *testing.T) {
			var data bytes.Buffer
			writer := zip.NewWriter(&data)
			header := &zip.FileHeader{Name: name, Method: zip.Store}
			if name == "sources/source.json" {
				header.SetMode(os.ModeSymlink | 0777)
			}
			w, err := writer.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("../outside")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			tmp := t.TempDir()
			archive := filepath.Join(tmp, "file.zip")
			if err := os.WriteFile(archive, data.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if err := unpack(archive, filepath.Join(tmp, "package")); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func fixtureServer(t *testing.T, data []byte, digest string, requested *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog.json":
			if err := json.NewEncoder(w).Encode(index{SchemaVersion: 1, Packages: []Entry{{
				PackageID: "sales", Version: "1.0.0", Publisher: "Fixture publisher",
				URL: "package.zip", SHA256: digest,
			}}}); err != nil {
				t.Error(err)
			}
		case "/package.zip":
			requested.Add(1)
			if _, err := w.Write(data); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
}

func zipFixture(t *testing.T, version string) []byte {
	t.Helper()
	store := fixtureLibrary(t)
	if err := store.CreateBrain(library.Brain{ID: "sales"}); err != nil {
		t.Fatal(err)
	}
	source, err := store.PutSource(library.Source{
		Title: "Synthetic guide", Redistribution: "CC0-1.0",
		Passages: []library.Passage{{Text: "Ask for a concrete example.", Locator: "line 2"}},
	}, []string{"sales"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "package")
	manifest, err := brainpackage.Export(store, brainpackage.ExportOptions{
		BrainID: "sales", Directory: dir, Version: version, Publisher: "Fixture publisher", Include: []string{source.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for _, name := range []string{"brain.json", manifest.Sources[0].File} {
		content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		f, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func fixtureLibrary(t *testing.T) *library.Store {
	t.Helper()
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestPinnedCatalogPreview(t *testing.T) {
	data := zipFixture(t, "1.0.0")
	server := fixtureServer(t, data, checksum(data), new(atomic.Int32))
	candidate, err := Load(t.Context(), server.URL, "sales", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	preview := candidate.Preview()
	if preview.ArchiveDigest != checksum(data) || len(preview.Digest) != 64 || preview.Version != "1.0.0" || preview.PublisherVerification != "unverified" || len(preview.Sources) != 1 {
		t.Fatalf("catalog preview: %+v", preview)
	}
}
