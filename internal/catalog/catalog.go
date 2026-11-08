// Package catalog installs versioned brain packages from a static catalog.
package catalog

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sam-bretz/braindex/internal/brainpackage"
	"github.com/sam-bretz/braindex/internal/library"
)

const (
	maxIndex     = 1 << 20
	maxArchive   = 64 << 20
	maxExtracted = 129 << 20
	maxEntries   = 2048
)

var versionPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

type Entry struct {
	PackageID string `json:"package_id"`
	Version   string `json:"version"`
	Publisher string `json:"publisher"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
}

type index struct {
	SchemaVersion int     `json:"schema_version"`
	Packages      []Entry `json:"packages"`
}

type client struct {
	address *url.URL
	http    *http.Client
}

func List(ctx context.Context, address string) ([]Entry, error) {
	c, err := newClient(address)
	if err != nil {
		return nil, err
	}
	return c.list(ctx)
}

func Install(ctx context.Context, store *library.Store, address, packageID, version, brainID string) (brainpackage.Manifest, error) {
	candidate, err := Load(ctx, address, packageID, version)
	if err != nil {
		return brainpackage.Manifest{}, err
	}
	if err := candidate.Install(store, brainID); err != nil {
		return brainpackage.Manifest{}, err
	}
	return candidate.Manifest, nil
}

func Load(ctx context.Context, address, packageID, version string) (brainpackage.Candidate, error) {
	c, err := newClient(address)
	if err != nil {
		return brainpackage.Candidate{}, err
	}
	if err := library.ValidateID(packageID); err != nil {
		return brainpackage.Candidate{}, err
	}
	if !versionPattern.MatchString(version) {
		return brainpackage.Candidate{}, errors.New("an explicit, valid package version is required")
	}
	entries, err := c.list(ctx)
	if err != nil {
		return brainpackage.Candidate{}, err
	}
	var selected *Entry
	for _, entry := range entries {
		if entry.PackageID == packageID && entry.Version == version {
			selected = &entry
			break
		}
	}
	if selected == nil {
		return brainpackage.Candidate{}, fmt.Errorf("catalog package %s@%s: %w", packageID, version, library.ErrNotFound)
	}
	work, err := os.MkdirTemp("", "braindex-catalog-*")
	if err != nil {
		return brainpackage.Candidate{}, err
	}
	defer os.RemoveAll(work)
	archive := filepath.Join(work, "package.zip")
	if err := c.download(ctx, *selected, archive); err != nil {
		return brainpackage.Candidate{}, err
	}
	directory := filepath.Join(work, "package")
	if err := unpack(archive, directory); err != nil {
		return brainpackage.Candidate{}, err
	}
	candidate, err := brainpackage.Open(directory)
	if err != nil {
		return brainpackage.Candidate{}, err
	}
	manifest := candidate.Manifest
	if manifest.PackageID != selected.PackageID || manifest.Version != selected.Version || manifest.Publisher != selected.Publisher {
		return brainpackage.Candidate{}, errors.New("package identity does not match catalog entry")
	}
	candidate.Origin = c.address.String() + "#" + packageID + "@" + version
	candidate.ArchiveDigest = selected.SHA256
	return candidate, nil
}

func newClient(address string) (*client, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	if !catalogURL(u) {
		return nil, errors.New("catalog requires an HTTPS URL without credentials, query, or fragment")
	}
	if u.Path == "" || strings.HasSuffix(u.Path, "/") {
		u = u.JoinPath("catalog.json")
	}
	return &client{address: u, http: &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(target *http.Request, previous []*http.Request) error {
			if len(previous) >= 5 || !sameOrigin(u, target.URL) {
				return errors.New("catalog redirect left its origin or exceeded the redirect limit")
			}
			return nil
		},
	}}, nil
}

func catalogURL(u *url.URL) bool {
	if u.Hostname() == "" || u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	return u.Scheme == "http" && (strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback())
}

func sameOrigin(base, target *url.URL) bool {
	return catalogURL(target) && base.Scheme == target.Scheme && strings.EqualFold(base.Host, target.Host)
}

func (c *client) list(ctx context.Context) ([]Entry, error) {
	body, err := c.get(ctx, c.address, maxIndex)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var listing index
	if err := decoder.Decode(&listing); err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("catalog index must contain one JSON object within the size limit")
	}
	if listing.SchemaVersion != 1 || listing.Packages == nil {
		return nil, errors.New("unsupported or incomplete catalog index")
	}
	seen := make(map[string]bool, len(listing.Packages))
	for i := range listing.Packages {
		entry := &listing.Packages[i]
		if err := library.ValidateID(entry.PackageID); err != nil {
			return nil, err
		}
		checksum, err := hex.DecodeString(entry.SHA256)
		if err != nil || len(checksum) != sha256.Size || entry.SHA256 != strings.ToLower(entry.SHA256) {
			return nil, fmt.Errorf("invalid SHA-256 for catalog package %q", entry.PackageID)
		}
		if !versionPattern.MatchString(entry.Version) || strings.TrimSpace(entry.Publisher) == "" || entry.URL == "" {
			return nil, fmt.Errorf("incomplete catalog package %q", entry.PackageID)
		}
		artifact, err := c.address.Parse(entry.URL)
		if err != nil || !sameOrigin(c.address, artifact) {
			return nil, fmt.Errorf("catalog artifact for %q must use the catalog origin", entry.PackageID)
		}
		entry.URL = artifact.String()
		key := entry.PackageID + "@" + entry.Version
		if seen[key] {
			return nil, fmt.Errorf("duplicate catalog package %q", key)
		}
		seen[key] = true
	}
	sort.Slice(listing.Packages, func(i, j int) bool {
		a, b := listing.Packages[i], listing.Packages[j]
		if a.PackageID == b.PackageID {
			return a.Version < b.Version
		}
		return a.PackageID < b.PackageID
	})
	return listing.Packages, nil
}

func (c *client) get(ctx context.Context, target *url.URL, limit int64) (io.ReadCloser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK || response.ContentLength > limit {
		response.Body.Close()
		return nil, fmt.Errorf("catalog GET %s: unexpected status or size (%s)", target.Redacted(), response.Status)
	}
	return &boundedBody{body: response.Body, remaining: limit}, nil
}

type boundedBody struct {
	body      io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		var extra [1]byte
		n, err := b.body.Read(extra[:])
		if n != 0 {
			return 0, errors.New("catalog response exceeds size limit")
		}
		return 0, err
	}
	if int64(len(data)) > b.remaining {
		data = data[:b.remaining]
	}
	n, err := b.body.Read(data)
	b.remaining -= int64(n)
	return n, err
}

func (b *boundedBody) Close() error { return b.body.Close() }

func (c *client) download(ctx context.Context, entry Entry, destination string) error {
	target, err := url.Parse(entry.URL)
	if err != nil {
		return err
	}
	body, err := c.get(ctx, target, maxArchive)
	if err != nil {
		return err
	}
	defer body.Close()
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, hash), body)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
		return errors.New("catalog archive SHA-256 does not match")
	}
	return nil
}

func unpack(archive, directory string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()
	if len(reader.File) > maxEntries {
		return errors.New("catalog archive has too many entries")
	}
	if err := os.MkdirAll(filepath.Join(directory, "sources"), 0700); err != nil {
		return err
	}
	seen := make(map[string]bool, len(reader.File))
	var total uint64
	for _, file := range reader.File {
		if seen[file.Name] {
			return fmt.Errorf("duplicate catalog archive entry %q", file.Name)
		}
		seen[file.Name] = true
		if file.Name == "sources/" && file.Mode().IsDir() {
			continue
		}
		if !file.Mode().IsRegular() || !archivePath(file.Name) {
			return fmt.Errorf("unsafe catalog archive entry %q", file.Name)
		}
		if file.UncompressedSize64 > maxExtracted-total {
			return errors.New("catalog archive exceeds extraction size limit")
		}
		if err := extract(file, filepath.Join(directory, filepath.FromSlash(file.Name)), file.UncompressedSize64); err != nil {
			return err
		}
		total += file.UncompressedSize64
	}
	if !seen["brain.json"] {
		return errors.New("catalog archive has no brain.json")
	}
	return nil
}

func archivePath(name string) bool {
	if name == "brain.json" {
		return true
	}
	if path.Dir(name) != "sources" || path.Ext(name) != ".json" {
		return false
	}
	id := strings.TrimSuffix(path.Base(name), ".json")
	return name == "sources/"+id+".json" && library.ValidateID(id) == nil
}

func extract(entry *zip.File, destination string, size uint64) error {
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(file, io.LimitReader(source, int64(size)+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if uint64(n) != size {
		return fmt.Errorf("invalid size of archive entry %q", entry.Name)
	}
	return nil
}
