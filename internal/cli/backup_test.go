package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sam-bretz/braindex/internal/library"
)

func TestBackupStatusReportsOriginalReferenceGaps(t *testing.T) {
	root, external := t.TempDir(), t.TempDir()
	store, err := library.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	externalPath := filepath.Join(external, "outside.txt")
	for _, name := range []string{filepath.Join(root, "inside.txt"), externalPath} {
		if err := os.WriteFile(name, []byte("content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(externalPath, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	sources := []library.Source{
		{ID: "inside", Path: "inside.txt"},
		{ID: "outside", Path: externalPath},
		{ID: "link", Path: "link.txt"},
		{ID: "missing", Path: "missing.txt"},
		{ID: "missing-external", Path: filepath.Join(external, "missing.txt")},
		{ID: "directory", Path: "."},
		{ID: "online", URL: "https://example.org/article"},
	}
	for _, source := range sources {
		if _, err := store.PutSource(source, nil); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := (command{root: root, out: &out}).backup([]string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	var report struct {
		References []backupReference `json:"references"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.References) != 5 {
		t.Fatalf("references = %+v", report.References)
	}
	byID := make(map[string]backupReference)
	for _, ref := range report.References {
		byID[ref.SourceID] = ref
	}
	if !byID["outside"].External || byID["outside"].Missing ||
		!byID["link"].External || byID["link"].Missing ||
		byID["missing"].External || !byID["missing"].Missing ||
		!byID["missing-external"].External || !byID["missing-external"].Missing ||
		byID["directory"].Problem == "" {
		t.Fatalf("reference flags = %+v", byID)
	}
}
