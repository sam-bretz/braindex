package brainpackage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sam-bretz/braindex/internal/library"
)

const (
	manifestFile  = "brain.json"
	schemaVersion = 1
	maxManifest   = 1 << 20
	maxSource     = 16 << 20
	maxPackage    = 128 << 20
)

var carrierGradeNAT = netip.MustParsePrefix("100.64.0.0/10")

var packageVersion = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

type ExportOptions struct {
	BrainID   string
	Directory string
	PackageID string
	Version   string
	Publisher string
	Include   []string
}

type Manifest struct {
	SchemaVersion int     `json:"schema_version"`
	PackageID     string  `json:"package_id"`
	Version       string  `json:"version"`
	Title         string  `json:"title"`
	Publisher     string  `json:"publisher,omitempty"`
	Sources       []Entry `json:"sources"`
}

type Entry struct {
	ID             string `json:"id"`
	URL            string `json:"url,omitempty"`
	File           string `json:"file,omitempty"`
	Title          string `json:"title,omitempty"`
	Author         string `json:"author,omitempty"`
	Date           string `json:"date,omitempty"`
	MIME           string `json:"mime,omitempty"`
	Redistribution string `json:"redistribution,omitempty"`
	Digest         string `json:"sha256,omitempty"`
}

type sourceData struct {
	Passages []library.Passage `json:"passages"`
}

func Export(store *library.Store, opts ExportOptions) (Manifest, error) {
	snapshot, err := store.Snapshot()
	if err != nil {
		return Manifest{}, err
	}
	var brain library.Brain
	for _, candidate := range snapshot.Brains {
		if candidate.ID == opts.BrainID {
			brain = candidate
			break
		}
	}
	if brain.ID == "" {
		return Manifest{}, fmt.Errorf("brain %q: %w", opts.BrainID, library.ErrNotFound)
	}
	if opts.PackageID == "" {
		opts.PackageID = brain.ID
	}
	if opts.Version == "" {
		opts.Version = "1.0.0"
	}
	manifest := Manifest{
		SchemaVersion: schemaVersion, PackageID: opts.PackageID, Version: opts.Version,
		Title: brain.Name, Publisher: opts.Publisher, Sources: []Entry{},
	}
	if err := validateHeader(manifest); err != nil {
		return Manifest{}, err
	}
	members := make(map[string]bool, len(brain.Sources))
	for _, id := range brain.Sources {
		members[id] = true
	}
	include := make(map[string]bool, len(opts.Include))
	for _, id := range opts.Include {
		if !members[id] {
			return Manifest{}, fmt.Errorf("source %q does not belong to brain %q", id, brain.ID)
		}
		include[id] = true
	}
	payloads := make(map[string][]byte, len(include))
	total := 0
	for _, source := range snapshot.Sources {
		if !members[source.ID] {
			continue
		}
		url := shareURL(source.URL)
		if include[source.ID] {
			if !redistributable(source.Redistribution) {
				return Manifest{}, fmt.Errorf("source %q has no redistribution designation", source.ID)
			}
			entry := Entry{
				ID:  "source-" + digest([]byte("bundle:"+source.ID)),
				URL: url, Title: source.Title, Author: source.Author, Date: source.Date,
				MIME: source.MIME, Redistribution: source.Redistribution,
			}
			entry.File = path.Join("sources", entry.ID+".json")
			if err := validateEntry(entry, false); err != nil {
				return Manifest{}, err
			}
			if err := validatePassages(source.Passages); err != nil {
				return Manifest{}, fmt.Errorf("source %q: %w", source.ID, err)
			}
			data, err := encodeJSON(sourceData{Passages: source.Passages})
			if err != nil {
				return Manifest{}, err
			}
			total += len(data)
			if len(data) > maxSource || total > maxPackage {
				return Manifest{}, errors.New("brain package exceeds content limit")
			}
			entry.Digest = digest(data)
			payloads[entry.File] = data
			manifest.Sources = append(manifest.Sources, entry)
		} else if url != "" {
			manifest.Sources = append(manifest.Sources, Entry{
				ID: "source-" + digest([]byte("reference:"+url)), URL: url,
			})
		}
	}
	// Multiple installed packages may carry the same reference. Keep a single
	// entry in the export, without collapsing explicitly selected bundles.
	seen := make(map[string]bool, len(manifest.Sources))
	entries := manifest.Sources[:0]
	for _, entry := range manifest.Sources {
		if !seen[entry.ID] {
			entries = append(entries, entry)
			seen[entry.ID] = true
		}
	}
	manifest.Sources = entries
	data, err := encodeJSON(manifest)
	if err != nil {
		return Manifest{}, err
	}
	if len(data) > maxManifest {
		return Manifest{}, errors.New("brain package manifest exceeds size limit")
	}
	if err := writeExport(opts.Directory, data, payloads); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func writeExport(directory string, manifest []byte, payloads map[string][]byte) error {
	// Mkdir claims a new destination exclusively. Publish the manifest last
	// so an interrupted export cannot be installed as a complete package.
	if err := os.Mkdir(directory, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		_ = os.Remove(directory)
		return err
	}
	defer root.Close()
	if len(payloads) != 0 {
		if err := root.Mkdir("sources", 0700); err != nil {
			return err
		}
		for name, data := range payloads {
			if err := root.WriteFile(name, data, 0600); err != nil {
				return err
			}
		}
	}
	file, err := root.OpenFile(".brain.json.tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(manifest)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return root.Rename(".brain.json.tmp", manifestFile)
}

func Validate(directory string) (Manifest, error) {
	manifest, _, _, err := readPackage(directory)
	return manifest, err
}

func Install(store *library.Store, directory, brainID, origin string) (Manifest, error) {
	manifest, sources, packageDigest, err := readPackage(directory)
	if err != nil {
		return Manifest{}, err
	}
	if brainID == "" {
		brainID = manifest.PackageID
	}
	if origin == "" {
		origin, err = filepath.Abs(directory)
		if err != nil {
			return Manifest{}, err
		}
	}
	err = store.InstallBrain(library.Brain{ID: brainID, Name: manifest.Title}, sources, library.PackageReceipt{
		PackageID: manifest.PackageID, Version: manifest.Version, Origin: origin, Digest: packageDigest,
	})
	if err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func readPackage(directory string) (Manifest, []library.Source, string, error) {
	var manifest Manifest
	info, err := os.Lstat(directory)
	if err != nil {
		return manifest, nil, "", err
	}
	if !info.IsDir() {
		return manifest, nil, "", errors.New("brain package must be a directory, not a symlink")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return manifest, nil, "", err
	}
	defer root.Close()
	data, err := readFile(root, manifestFile, maxManifest)
	if err != nil {
		return manifest, nil, "", err
	}
	if err := decodeJSON(data, &manifest); err != nil {
		return manifest, nil, "", fmt.Errorf("read brain package manifest: %w", err)
	}
	if err := validateHeader(manifest); err != nil {
		return manifest, nil, "", err
	}
	packageDigest := digest(data)
	seen := make(map[string]bool, len(manifest.Sources))
	sources := make([]library.Source, 0, len(manifest.Sources))
	total := 0
	for _, entry := range manifest.Sources {
		if err := validateEntry(entry, true); err != nil {
			return manifest, nil, "", err
		}
		if seen[entry.ID] {
			return manifest, nil, "", fmt.Errorf("duplicate package source %q", entry.ID)
		}
		seen[entry.ID] = true
		source := library.Source{
			ID: "package-" + digest([]byte(packageDigest+":"+entry.ID)), URL: entry.URL,
			Title: entry.Title, Author: entry.Author, Date: entry.Date,
			MIME: entry.MIME, Redistribution: entry.Redistribution,
		}
		if entry.File != "" {
			content, err := readFile(root, entry.File, maxSource)
			if err != nil {
				return manifest, nil, "", err
			}
			total += len(content)
			if total > maxPackage {
				return manifest, nil, "", errors.New("brain package exceeds content limit")
			}
			if digest(content) != entry.Digest {
				return manifest, nil, "", fmt.Errorf("checksum mismatch for package source %q", entry.ID)
			}
			var payload sourceData
			if err := decodeJSON(content, &payload); err != nil {
				return manifest, nil, "", fmt.Errorf("decode package source %q: %w", entry.ID, err)
			}
			if err := validatePassages(payload.Passages); err != nil {
				return manifest, nil, "", fmt.Errorf("package source %q: %w", entry.ID, err)
			}
			source.Passages = payload.Passages
		}
		sources = append(sources, source)
	}
	return manifest, sources, packageDigest, nil
}

func validateHeader(manifest Manifest) error {
	if manifest.SchemaVersion != schemaVersion {
		return fmt.Errorf("unsupported brain package schema version %d", manifest.SchemaVersion)
	}
	if err := library.ValidateID(manifest.PackageID); err != nil {
		return err
	}
	if !packageVersion.MatchString(manifest.Version) {
		return fmt.Errorf("invalid package version %q", manifest.Version)
	}
	if strings.TrimSpace(manifest.Title) == "" || manifest.Sources == nil {
		return errors.New("brain package requires a title and a sources array")
	}
	return nil
}

func validateEntry(entry Entry, requireDigest bool) error {
	if err := library.ValidateID(entry.ID); err != nil {
		return err
	}
	if entry.URL != "" && shareURL(entry.URL) != entry.URL {
		return fmt.Errorf("unsafe source URL in package entry %q", entry.ID)
	}
	if entry.File == "" {
		if entry.URL == "" || entry.Title != "" || entry.Author != "" || entry.Date != "" ||
			entry.MIME != "" || entry.Redistribution != "" || entry.Digest != "" {
			return fmt.Errorf("reference entry %q must contain only an ID and a shareable URL", entry.ID)
		}
		return nil
	}
	if entry.File != path.Join("sources", entry.ID+".json") {
		return fmt.Errorf("invalid path for package entry %q", entry.ID)
	}
	if strings.TrimSpace(entry.Title) == "" || !redistributable(entry.Redistribution) {
		return fmt.Errorf("package entry %q requires a title and redistribution designation", entry.ID)
	}
	if requireDigest {
		checksum, err := hex.DecodeString(entry.Digest)
		if err != nil || len(checksum) != sha256.Size || strings.ToLower(entry.Digest) != entry.Digest {
			return fmt.Errorf("invalid checksum for package entry %q", entry.ID)
		}
	}
	return nil
}

func validatePassages(passages []library.Passage) error {
	if len(passages) == 0 {
		return errors.New("bundled source requires at least one passage")
	}
	for _, passage := range passages {
		if strings.TrimSpace(passage.Text) == "" || strings.TrimSpace(passage.Locator) == "" {
			return errors.New("each bundled passage requires text and a locator")
		}
	}
	return nil
}

func redistributable(license string) bool {
	license = strings.ToLower(strings.TrimSpace(license))
	return license != "" && license != "private"
}

func shareURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil ||
		u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || strings.Contains(raw, "#") {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip := net.ParseIP(host); ip != nil {
		address, _ := netip.AddrFromSlice(ip)
		if ip.IsPrivate() || !ip.IsGlobalUnicast() || carrierGradeNAT.Contains(address.Unmap()) {
			return ""
		}
	} else if !strings.Contains(host, ".") || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return ""
	}
	return raw
}

func readFile(root *os.Root, name string, limit int64) ([]byte, error) {
	if dir := path.Dir(name); dir != "." {
		info, err := root.Lstat(dir)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("package path %q is not a directory", dir)
		}
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("package path %q is not a regular file", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("package file %q exceeds size limit", name)
	}
	return data, nil
}

func decodeJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("package file must contain one JSON value")
	}
	return nil
}

func encodeJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	return append(data, '\n'), err
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
