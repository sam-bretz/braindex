package index

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sam-bretz/braindex/internal/library"
)

const (
	embeddingFile       = "embeddings.sqlite"
	embeddingVersion    = 1
	embeddingChunkRunes = 600
	embeddingBatch      = 24
)

var (
	ErrEmbeddingsMissing = errors.New("embedding index is missing; run braindex index embed")
	ErrEmbeddingsStale   = errors.New("embedding index is stale; run braindex index embed")
)

type SemanticIndex struct {
	db      *sql.DB
	query   []float32
	minimum float64
}

type VectorMatch struct {
	Match
	Similarity float64
}

func RebuildEmbeddings(ctx context.Context, store *library.Store, name string, minimum float64) error {
	if math.IsNaN(minimum) || minimum < 0 || minimum > 1 {
		return errors.New("minimum similarity must be between 0 and 1")
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		return err
	}
	e, err := localEmbedder()
	if err != nil {
		return err
	}
	defer e.client.CloseIdleConnections()
	model, err := e.model(ctx, name)
	if err != nil {
		return err
	}

	dir, err := os.MkdirTemp(store.MetaDir(), ".embeddings-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, embeddingFile)
	if err := buildEmbeddings(ctx, file, snapshot.Sources, e, model, minimum); err != nil {
		return err
	}
	current, err := e.model(ctx, model.name)
	if err != nil {
		return err
	}
	if current.digest != model.digest {
		return errors.New("Ollama model changed during embedding build; retry")
	}
	return os.Rename(file, filepath.Join(store.MetaDir(), embeddingFile))
}

func buildEmbeddings(ctx context.Context, file string, sources []library.Source, e *embedder, model embeddingModel, minimum float64) (err error) {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn(file, "rw"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE settings (version INTEGER, generation TEXT, model TEXT, digest TEXT, dimension INTEGER, minimum REAL);
		CREATE TABLE vectors (source_id TEXT, revision TEXT, locator TEXT, body TEXT, vector BLOB);
		CREATE INDEX vector_source ON vectors(source_id);
	`); err != nil {
		return err
	}
	insert, err := tx.PrepareContext(ctx, "INSERT INTO vectors VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer insert.Close()

	var batch []Match
	dimension := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		input := make([]string, len(batch))
		for i, item := range batch {
			input[i] = item.Text
		}
		vectors, err := e.vectors(ctx, model, input, false)
		if err != nil {
			return err
		}
		for i, vector := range vectors {
			if dimension == 0 {
				dimension = len(vector)
			}
			if len(vector) != dimension {
				return errors.New("Ollama returned inconsistent embedding dimensions")
			}
			item := batch[i]
			if _, err := insert.ExecContext(ctx, item.SourceID, item.Revision, item.Locator, item.Text, packVector(vector)); err != nil {
				return err
			}
		}
		batch = batch[:0]
		return nil
	}
	for _, source := range sources {
		for _, passage := range source.Passages {
			for _, chunk := range embeddingChunks(passage.Text) {
				batch = append(batch, Match{SourceID: source.ID, Revision: source.Revision, Text: chunk, Locator: passage.Locator})
				if len(batch) == embeddingBatch {
					if err := flush(); err != nil {
						return err
					}
				}
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO settings VALUES (?, ?, ?, ?, ?, ?)",
		embeddingVersion, generation(sources), model.name, model.digest, dimension, minimum); err != nil {
		return err
	}
	return tx.Commit()
}

func embeddingChunks(text string) []string {
	runes := []rune(strings.TrimSpace(text))
	var chunks []string
	for len(runes) > 0 {
		end := min(len(runes), embeddingChunkRunes)
		if end < len(runes) {
			for i := end; i > end/2; i-- {
				if runes[i] == ' ' || runes[i] == '\n' || runes[i] == '\t' {
					end = i
					break
				}
			}
		}
		if chunk := strings.TrimSpace(string(runes[:end])); chunk != "" {
			chunks = append(chunks, chunk)
		}
		runes = runes[end:]
	}
	return chunks
}

func packVector(values []float32) []byte {
	data := make([]byte, len(values)*4)
	for i, value := range values {
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(value))
	}
	return data
}

func OpenSemantic(ctx context.Context, store *library.Store, sources []library.Source, query string) (_ *SemanticIndex, err error) {
	db, err := openEmbeddingDB(store)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			db.Close()
		}
	}()
	settings, err := readEmbeddingSettings(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEmbeddingsStale, err)
	}
	if !settings.current(sources) {
		return nil, ErrEmbeddingsStale
	}
	if settings.dimension == 0 {
		return &SemanticIndex{db: db, minimum: settings.minimum}, nil
	}
	e, err := localEmbedder()
	if err != nil {
		return nil, err
	}
	defer e.client.CloseIdleConnections()
	model, err := e.model(ctx, settings.model)
	if err != nil {
		return nil, err
	}
	if model.digest != settings.digest {
		return nil, ErrEmbeddingsStale
	}
	vectors, err := e.vectors(ctx, model, []string{query}, true)
	if err != nil {
		return nil, err
	}
	if len(vectors[0]) != settings.dimension {
		return nil, ErrEmbeddingsStale
	}
	return &SemanticIndex{db: db, query: vectors[0], minimum: settings.minimum}, nil
}

func (s *SemanticIndex) Close() error { return s.db.Close() }

func (s *SemanticIndex) Search(ctx context.Context, sourceIDs []string, limit int) ([]VectorMatch, error) {
	results := []VectorMatch{}
	if len(s.query) == 0 || limit <= 0 {
		return results, nil
	}
	scope, err := json.Marshal(sourceIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_id, revision, locator, body, vector FROM vectors
		WHERE source_id IN (SELECT value FROM json_each(?)) ORDER BY rowid`, string(scope))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item VectorMatch
		var packed []byte
		if err := rows.Scan(&item.SourceID, &item.Revision, &item.Locator, &item.Text, &packed); err != nil {
			return nil, err
		}
		if len(packed) != len(s.query)*4 {
			return nil, ErrEmbeddingsStale
		}
		for i, value := range s.query {
			item.Similarity += float64(value) * float64(math.Float32frombits(binary.LittleEndian.Uint32(packed[i*4:])))
		}
		if item.Similarity < s.minimum || math.IsNaN(item.Similarity) {
			continue
		}
		at := sort.Search(len(results), func(i int) bool { return results[i].Similarity < item.Similarity })
		if at == limit {
			continue
		}
		if len(results) < limit {
			results = append(results, VectorMatch{})
		}
		copy(results[at+1:], results[at:])
		results[at] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
