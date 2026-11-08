package library

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

const metadataDir = ".braindex"

var (
	idPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

type Brain struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind,omitempty"`
	Sources []string `json:"sources"`
}

type Passage struct {
	Text    string `json:"text"`
	Locator string `json:"locator"`
}

type Source struct {
	ID             string    `json:"id"`
	Revision       string    `json:"revision"`
	Title          string    `json:"title"`
	Author         string    `json:"author,omitempty"`
	Date           string    `json:"date,omitempty"`
	URL            string    `json:"url,omitempty"`
	Path           string    `json:"path,omitempty"`
	Status         string    `json:"status"`
	MIME           string    `json:"mime,omitempty"`
	Redistribution string    `json:"redistribution,omitempty"`
	Passages       []Passage `json:"passages"`
}

type Snapshot struct {
	Brains  []Brain
	Sources []Source
}

type sourceRecord struct {
	Current     string   `json:"current"`
	Revisions   []string `json:"revisions"`
	Fingerprint string   `json:"fingerprint,omitempty"`
}

type PackageReceipt struct {
	PackageID string `json:"package_id"`
	Version   string `json:"version"`
	Origin    string `json:"origin"`
	Digest    string `json:"digest"`
}

type catalog struct {
	Version  int                       `json:"version"`
	Brains   map[string]Brain          `json:"brains"`
	Sources  map[string]sourceRecord   `json:"sources"`
	Origins  map[string]string         `json:"origins"`
	Packages map[string]PackageReceipt `json:"packages,omitempty"`
}

type Store struct {
	Root string
}

func ValidateID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("invalid ID %q: use up to 128 lowercase letters, digits, hyphens, or underscores, starting with a letter or digit", id)
	}
	return nil
}

func Init(root string) (*Store, error) {
	store, err := storeAt(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(store.MetaDir(), 0700); err != nil {
		return nil, err
	}
	if err := store.withLock(func() error {
		_, err := store.load()
		switch {
		case err == nil:
			return nil
		case !errors.Is(err, os.ErrNotExist):
			return err
		}
		return store.save(&catalog{
			Version: 1,
			Brains:  map[string]Brain{},
			Sources: map[string]sourceRecord{},
			Origins: map[string]string{},
		})
	}); err != nil {
		return nil, err
	}
	return store, nil
}

func Open(root string) (*Store, error) {
	store, err := storeAt(root)
	if err != nil {
		return nil, err
	}
	if _, err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func storeAt(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("library root is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Store{Root: root}, nil
}

func (s *Store) MetaDir() string {
	return filepath.Join(s.Root, metadataDir)
}

func (s *Store) CreateBrain(brain Brain) error {
	if err := ValidateID(brain.ID); err != nil {
		return err
	}
	brain.Name = strings.TrimSpace(brain.Name)
	if brain.Name == "" {
		brain.Name = brain.ID
	}
	return s.update(func(c *catalog) error {
		if old, exists := c.Brains[brain.ID]; exists {
			if old.Name == brain.Name && old.Kind == brain.Kind && len(brain.Sources) == 0 {
				return nil
			}
			return fmt.Errorf("brain %q: %w", brain.ID, ErrExists)
		}
		brain.Sources = uniqueSorted(brain.Sources)
		for _, id := range brain.Sources {
			if _, ok := c.Sources[id]; !ok {
				return fmt.Errorf("source %q: %w", id, ErrNotFound)
			}
		}
		c.Brains[brain.ID] = brain
		return nil
	})
}

func (s *Store) Brains() ([]Brain, error) {
	c, err := s.load()
	if err != nil {
		return nil, err
	}
	return sortedBrains(c), nil
}

func sortedBrains(c *catalog) []Brain {
	brains := make([]Brain, 0, len(c.Brains))
	for _, brain := range c.Brains {
		brains = append(brains, brain)
	}
	sort.Slice(brains, func(i, j int) bool { return brains[i].ID < brains[j].ID })
	return brains
}

func (s *Store) PutSource(source Source, brainIDs []string) (Source, error) {
	return s.putSource(source, brainIDs, false)
}

// PutReference links an existing URL revision or records a new metadata-only
// source. Resolving and adding the memberships occur under the same lock.
func (s *Store) PutReference(source Source, brainIDs []string) (Source, error) {
	if strings.TrimSpace(source.URL) == "" || source.Path != "" || len(source.Passages) != 0 {
		return Source{}, errors.New("source reference requires a URL and no local path or passages")
	}
	return s.putSource(source, brainIDs, true)
}

func (s *Store) putSource(source Source, brainIDs []string, referenceOnly bool) (Source, error) {
	var err error
	source, err = s.prepareSource(source)
	if err != nil {
		return Source{}, err
	}
	err = s.update(func(c *catalog) error {
		for _, id := range brainIDs {
			if _, ok := c.Brains[id]; !ok {
				return fmt.Errorf("brain %q: %w", id, ErrNotFound)
			}
		}
		origin := sourceOrigin(source)
		if existing := c.Origins[origin]; referenceOnly && existing != "" {
			source, err = s.readRevision(existing, c.Sources[existing].Current)
		} else if record, exists := c.Sources[source.ID]; referenceOnly && exists {
			current, readErr := s.readRevision(source.ID, record.Current)
			if readErr != nil {
				return readErr
			}
			if current.URL != source.URL {
				return fmt.Errorf("source %q belongs to a different URL", source.ID)
			}
			source = current
		} else {
			source, err = s.recordSource(c, source, origin)
		}
		if err != nil {
			return err
		}
		for _, id := range brainIDs {
			brain := c.Brains[id]
			brain.Sources = uniqueSorted(append(brain.Sources, source.ID))
			c.Brains[id] = brain
		}
		return nil
	})
	if err != nil {
		return Source{}, err
	}
	return source, nil
}

// InstallBrain publishes an entire validated package and its receipt together.
// Package sources use explicit namespaced IDs and do not change the origins of
// ordinary local or connector imports.
func (s *Store) InstallBrain(brain Brain, sources []Source, receipt PackageReceipt) error {
	if err := ValidateID(brain.ID); err != nil {
		return err
	}
	if strings.TrimSpace(brain.Name) == "" || receipt.PackageID == "" ||
		receipt.Version == "" || receipt.Origin == "" || receipt.Digest == "" {
		return errors.New("brain name and package receipt are required")
	}
	return s.update(func(c *catalog) error {
		if _, exists := c.Brains[brain.ID]; exists {
			if c.Packages[brain.ID] == receipt {
				return nil
			}
			return fmt.Errorf("brain %q: %w", brain.ID, ErrExists)
		}
		brain.Sources = nil
		for _, source := range sources {
			if source.ID == "" || source.Path != "" {
				return errors.New("package sources require an ID and cannot reference local paths")
			}
			source, err := s.prepareSource(source)
			if err != nil {
				return err
			}
			if _, err := s.recordSource(c, source, ""); err != nil {
				return err
			}
			brain.Sources = append(brain.Sources, source.ID)
		}
		brain.Sources = uniqueSorted(brain.Sources)
		c.Brains[brain.ID] = brain
		if c.Packages == nil {
			c.Packages = make(map[string]PackageReceipt)
		}
		c.Packages[brain.ID] = receipt
		return nil
	})
}

func (s *Store) prepareSource(source Source) (Source, error) {
	if source.ID != "" {
		if err := ValidateID(source.ID); err != nil {
			return Source{}, err
		}
	}
	source.Path = s.relativePath(source.Path)
	if source.Status == "" {
		source.Status = "unavailable"
		if len(source.Passages) != 0 {
			source.Status = "ready"
		}
	}
	if source.Passages == nil {
		source.Passages = []Passage{}
	}
	if source.Title == "" {
		source.Title = source.URL
		if source.Path != "" {
			source.Title = filepath.Base(source.Path)
		}
	}
	for _, passage := range source.Passages {
		if strings.TrimSpace(passage.Text) == "" || strings.TrimSpace(passage.Locator) == "" {
			return Source{}, errors.New("each passage requires text and a locator")
		}
	}
	if source.Title == "" {
		return Source{}, errors.New("source title is required")
	}
	source.Revision = ""
	return source, nil
}

func (s *Store) recordSource(c *catalog, source Source, origin string) (Source, error) {
	fingerprint := ""
	if len(source.Passages) != 0 {
		fingerprint = hashJSON(source.Passages)
	}
	if source.ID == "" {
		var err error
		source, err = s.assignID(c, source, origin, fingerprint)
		if err != nil {
			return Source{}, err
		}
	}
	// Distinct connector IDs can refer to the same link. Preserve their
	// independent provenance and leave the first origin mapping in place
	// for subsequent imports that do not supply a connector ID.
	source.Revision = ""
	source.Revision = hashJSON(source)
	record := c.Sources[source.ID]
	if !contains(record.Revisions, source.Revision) {
		if err := writeJSON(s.revisionPath(source.ID, source.Revision), source); err != nil {
			return Source{}, err
		}
		record.Revisions = append(record.Revisions, source.Revision)
	}
	record.Current = source.Revision
	record.Fingerprint = fingerprint
	c.Sources[source.ID] = record
	if origin != "" && c.Origins[origin] == "" {
		c.Origins[origin] = source.ID
	}
	return source, nil
}

func (s *Store) assignID(c *catalog, source Source, origin, fingerprint string) (Source, error) {
	if id := c.Origins[origin]; id != "" {
		record := c.Sources[id]
		if source.URL == "" && fingerprint != "" && record.Fingerprint == fingerprint {
			existing, err := s.readRevision(id, record.Current)
			if err != nil {
				return Source{}, err
			}
			if existing.Path != source.Path {
				return existing, nil
			}
		}
		source.ID = id
		return source, nil
	}
	// Different provider URLs retain their own provenance. Deduplicate local
	// copies by passages, using a stable ID order when more than one matches.
	if source.URL == "" && fingerprint != "" {
		for _, id := range sortedIDs(c.Sources) {
			record := c.Sources[id]
			if record.Fingerprint == fingerprint {
				existing, err := s.readRevision(id, record.Current)
				if err != nil {
					return Source{}, err
				}
				if existing.URL == "" {
					return existing, nil
				}
			}
		}
	}
	key := origin
	if key == "" {
		key = hashJSON(source)
	}
	source.ID = "source-" + hashJSON(key)
	return source, nil
}

func sourceOrigin(source Source) string {
	if source.URL != "" {
		return "url:" + source.URL
	}
	if source.Path != "" {
		return "file:" + source.Path
	}
	return ""
}

func (s *Store) relativePath(path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(filepath.Clean(path))
	}
	rel, err := filepath.Rel(s.Root, path)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return filepath.Clean(path)
}

func (s *Store) Source(id string) (Source, error) {
	c, err := s.load()
	if err != nil {
		return Source{}, err
	}
	record, ok := c.Sources[id]
	if !ok {
		return Source{}, fmt.Errorf("source %q: %w", id, ErrNotFound)
	}
	return s.readRevision(id, record.Current)
}

func (s *Store) SourceRevision(id, revision string) (Source, error) {
	c, err := s.load()
	if err != nil {
		return Source{}, err
	}
	if !contains(c.Sources[id].Revisions, revision) {
		return Source{}, fmt.Errorf("revision %q of source %q: %w", revision, id, ErrNotFound)
	}
	return s.readRevision(id, revision)
}

func (s *Store) Sources() ([]Source, error) {
	c, err := s.load()
	if err != nil {
		return nil, err
	}
	return s.sources(c)
}

func (s *Store) Snapshot() (Snapshot, error) {
	c, err := s.load()
	if err != nil {
		return Snapshot{}, err
	}
	sources, err := s.sources(c)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Brains: sortedBrains(c), Sources: sources}, nil
}

func (s *Store) sources(c *catalog) ([]Source, error) {
	sources := make([]Source, 0, len(c.Sources))
	for _, id := range sortedIDs(c.Sources) {
		source, err := s.readRevision(id, c.Sources[id].Current)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func (s *Store) revisionPath(id, revision string) string {
	return filepath.Join(s.MetaDir(), "sources", id, revision+".json")
}

func (s *Store) readRevision(id, revision string) (Source, error) {
	if err := ValidateID(id); err != nil {
		return Source{}, err
	}
	if _, err := hex.DecodeString(revision); err != nil || len(revision) != sha256.Size*2 {
		return Source{}, fmt.Errorf("invalid revision %q", revision)
	}
	var source Source
	if err := readJSON(s.revisionPath(id, revision), &source); err != nil {
		return Source{}, err
	}
	if source.ID != id || source.Revision != revision {
		return Source{}, fmt.Errorf("source revision metadata does not match %s/%s", id, revision)
	}
	revisionSource := source
	revisionSource.Revision = ""
	if hashJSON(revisionSource) != revision {
		return Source{}, fmt.Errorf("source revision content does not match %s/%s", id, revision)
	}
	return source, nil
}

func (s *Store) load() (*catalog, error) {
	var c catalog
	if err := readJSON(filepath.Join(s.MetaDir(), "library.json"), &c); err != nil {
		return nil, err
	}
	if c.Version != 1 || c.Brains == nil || c.Sources == nil || c.Origins == nil {
		return nil, errors.New("unsupported or incomplete library manifest")
	}
	return &c, nil
}

func (s *Store) save(c *catalog) error {
	return writeJSON(filepath.Join(s.MetaDir(), "library.json"), c)
}

func (s *Store) update(fn func(*catalog) error) error {
	return s.withLock(func() error {
		c, err := s.load()
		if err != nil {
			return err
		}
		if err := fn(c); err != nil {
			return err
		}
		return s.save(c)
	})
}

func (s *Store) withLock(fn func() error) error {
	file, err := os.OpenFile(filepath.Join(s.MetaDir(), "library.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	return fn()
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

func hashJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err) // Only strings, passages, and source records are hashed.
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sortedIDs[V any](records map[string]V) []string {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return sortedIDs(set)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
