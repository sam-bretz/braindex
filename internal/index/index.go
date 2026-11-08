// Package index maintains Braindex's disposable full-text search database.
package index

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/sam-bretz/braindex/internal/library"

	_ "modernc.org/sqlite"
)

var ErrStale = errors.New("search index is missing or stale; run braindex index rebuild")

const indexVersion = "1"

type Index struct {
	db *sql.DB
}

type Match struct {
	SourceID string
	Revision string
	Text     string
	Locator  string
}

func Rebuild(store *library.Store) error {
	snapshot, err := store.Snapshot()
	if err != nil {
		return err
	}

	dir := store.MetaDir()
	staging, err := os.MkdirTemp(dir, ".index-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	filename := filepath.Join(staging, "index.sqlite")
	if err := build(filename, snapshot.Sources); err != nil {
		return err
	}
	return os.Rename(filename, filepath.Join(dir, "index.sqlite"))
}

func build(filename string, sources []library.Source) (err error) {
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn(filename, "rw"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		CREATE TABLE generation (value TEXT NOT NULL);
		CREATE VIRTUAL TABLE passages USING fts5(body, source_id UNINDEXED, revision UNINDEXED, locator UNINDEXED);
	`); err != nil {
		return fmt.Errorf("initialize FTS5 index: %w", err)
	}
	if _, err := tx.Exec("INSERT INTO generation VALUES (?)", generation(sources)); err != nil {
		return err
	}
	stmt, err := tx.Prepare("INSERT INTO passages VALUES (?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, source := range sources {
		for _, passage := range source.Passages {
			if strings.TrimSpace(passage.Text) == "" {
				continue
			}
			if _, err := stmt.Exec(passage.Text, source.ID, source.Revision, passage.Locator); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func Open(store *library.Store, sources []library.Source) (*Index, error) {
	filename := filepath.Join(store.MetaDir(), "index.sqlite")
	if _, err := os.Stat(filename); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrStale
		}
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn(filename, "ro"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var got string
	if err := db.QueryRow("SELECT value FROM generation").Scan(&got); err != nil {
		db.Close()
		return nil, fmt.Errorf("%w: %v", ErrStale, err)
	}
	if got != generation(sources) {
		db.Close()
		return nil, ErrStale
	}
	return &Index{db: db}, nil
}

func (idx *Index) Close() error {
	return idx.db.Close()
}

func (idx *Index) Search(query string, sourceIDs []string, limit int) ([]Match, error) {
	matches := []Match{}
	terms := Terms(query)
	if len(terms) == 0 || len(sourceIDs) == 0 || limit <= 0 {
		return matches, nil
	}
	for i, term := range terms {
		terms[i] = `"` + term + `"`
	}
	scope, err := json.Marshal(sourceIDs)
	if err != nil {
		return nil, err
	}
	rows, err := idx.db.Query(`
		SELECT source_id, revision, body, locator FROM passages
		WHERE passages MATCH ? AND source_id IN (SELECT value FROM json_each(?))
		ORDER BY bm25(passages), source_id, rowid
		LIMIT ?`, strings.Join(terms, " OR "), string(scope), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var match Match
		if err := rows.Scan(&match.SourceID, &match.Revision, &match.Text, &match.Locator); err != nil {
			return nil, err
		}
		matches = append(matches, match)
	}
	return matches, rows.Err()
}

// Terms tokenizes user text without interpreting it as FTS query syntax.
func Terms(text string) []string {
	terms := make([]string, 0, 8)
	seen := map[string]bool{}
	for _, term := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r)
	}) {
		if seen[term] || isQueryStopWord(term) {
			continue
		}
		terms = append(terms, term)
		seen[term] = true
		if len(terms) == 64 {
			break
		}
	}
	return terms
}

// Ignore common question scaffolding so an unrelated question does not
// retrieve evidence merely because a passage contains "the" or "for".
func isQueryStopWord(term string) bool {
	switch term {
	case "a", "an", "and", "are", "at", "be", "by", "can", "do", "does", "for", "from", "how", "i", "in", "is", "it", "of", "on", "or", "should", "that", "the", "this", "to", "was", "what", "when", "where", "which", "who", "why", "will", "with", "would", "you":
		return true
	default:
		return false
	}
}

func generation(sources []library.Source) string {
	type revision struct{ ID, Revision string }
	versions := make([]revision, 0, len(sources))
	for _, source := range sources {
		versions = append(versions, revision{source.ID, source.Revision})
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].ID < versions[j].ID })
	data, _ := json.Marshal(versions)
	return fmt.Sprintf("%s:%x", indexVersion, sha256.Sum256(data))
}

func dsn(filename, mode string) string {
	return (&url.URL{Scheme: "file", Path: filename, RawQuery: "mode=" + mode}).String()
}
