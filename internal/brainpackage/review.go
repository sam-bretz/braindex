package brainpackage

import (
	"path/filepath"

	"github.com/sam-bretz/braindex/internal/library"
)

// Candidate contains a fully validated package and the evidence of its origin.
type Candidate struct {
	Manifest      Manifest
	Origin        string
	Digest        string
	Commit        string
	ArchiveDigest string
	sources       []library.Source
}

type InventoryItem struct {
	ID         string `json:"id"`
	Title      string `json:"title,omitempty"`
	URL        string `json:"url,omitempty"`
	Searchable bool   `json:"searchable"`
}

type Preview struct {
	PackageID             string          `json:"package_id"`
	Version               string          `json:"version"`
	Title                 string          `json:"title"`
	Publisher             string          `json:"publisher,omitempty"`
	PublisherVerification string          `json:"publisher_verification"`
	Origin                string          `json:"origin"`
	Digest                string          `json:"manifest_sha256"`
	Commit                string          `json:"git_commit,omitempty"`
	ArchiveDigest         string          `json:"archive_sha256,omitempty"`
	Searchable            int             `json:"searchable_sources"`
	References            int             `json:"reference_only_sources"`
	Sources               []InventoryItem `json:"sources"`
}

func Open(directory string) (Candidate, error) {
	manifest, sources, digest, err := readPackage(directory)
	if err != nil {
		return Candidate{}, err
	}
	origin, err := filepath.Abs(directory)
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{Manifest: manifest, Origin: origin, Digest: digest, sources: sources}, nil
}

func (c Candidate) Preview() Preview {
	result := Preview{
		PackageID: c.Manifest.PackageID, Version: c.Manifest.Version, Title: c.Manifest.Title,
		Publisher: c.Manifest.Publisher, PublisherVerification: "unverified",
		Origin: c.Origin, Digest: c.Digest, Commit: c.Commit, ArchiveDigest: c.ArchiveDigest,
		Sources: make([]InventoryItem, 0, len(c.Manifest.Sources)),
	}
	if result.Publisher == "" {
		result.PublisherVerification = "not_declared"
	}
	for _, entry := range c.Manifest.Sources {
		item := InventoryItem{ID: entry.ID, Title: entry.Title, URL: entry.URL, Searchable: entry.File != ""}
		if item.Searchable {
			result.Searchable++
		} else {
			result.References++
		}
		result.Sources = append(result.Sources, item)
	}
	return result
}

func (c Candidate) receipt() library.PackageReceipt {
	return library.PackageReceipt{
		PackageID: c.Manifest.PackageID, Version: c.Manifest.Version, Origin: c.Origin, Digest: c.Digest,
	}
}

func (c Candidate) Install(store *library.Store, brainID string) error {
	if brainID == "" {
		brainID = c.Manifest.PackageID
	}
	return store.InstallBrain(library.Brain{ID: brainID, Name: c.Manifest.Title}, c.packageSources(), c.receipt())
}

func (c Candidate) packageSources() []library.PackageSource {
	sources := make([]library.PackageSource, 0, len(c.sources))
	for i, source := range c.sources {
		sources = append(sources, library.PackageSource{Key: c.Manifest.Sources[i].ID, Source: source})
	}
	return sources
}
