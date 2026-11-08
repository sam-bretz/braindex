package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sam-bretz/braindex/internal/library"
)

type backupReference struct {
	SourceID string `json:"source_id"`
	Revision string `json:"revision"`
	Path     string `json:"path"`
	External bool   `json:"external"`
	Missing  bool   `json:"missing"`
	Problem  string `json:"problem,omitempty"`
}

func (c command) backup(args []string) error {
	if len(args) == 0 || args[0] != "status" || len(args) > 2 ||
		(len(args) == 2 && args[1] != "--json") {
		return errors.New("usage: braindex backup status [--json]")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(store.Root)
	if err != nil {
		return fmt.Errorf("resolve library root: %w", err)
	}
	sources, err := store.Sources()
	if err != nil {
		return err
	}
	references := make([]backupReference, 0)
	for _, source := range sources {
		if source.Path == "" {
			continue
		}
		ref := originalReference(store.Root, resolvedRoot, source)
		if ref.External || ref.Missing || ref.Problem != "" {
			references = append(references, ref)
		}
	}
	return c.json(struct {
		References []backupReference `json:"references"`
	}{References: references})
}

func originalReference(root, resolvedRoot string, source library.Source) backupReference {
	ref := backupReference{SourceID: source.ID, Revision: source.Revision, Path: source.Path}
	path := filepath.FromSlash(source.Path)
	ref.External = filepath.IsAbs(path)
	if !ref.External {
		path = filepath.Join(root, path)
		ref.External = outsideRoot(root, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			ref.Missing = true
		} else {
			ref.Problem = err.Error()
		}
		return ref
	}
	ref.External = ref.External || outsideRoot(resolvedRoot, resolved)
	info, err := os.Stat(resolved)
	switch {
	case errors.Is(err, os.ErrNotExist):
		ref.Missing = true
	case err != nil:
		ref.Problem = err.Error()
	case !info.Mode().IsRegular():
		ref.Problem = "original is not a regular file"
	}
	return ref
}

func outsideRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
