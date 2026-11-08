package index

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"github.com/sam-bretz/braindex/internal/library"
)

type Health struct {
	State      string `json:"state"`
	Model      string `json:"model,omitempty"`
	Dimensions int    `json:"dimensions,omitempty"`
	Runtime    string `json:"runtime,omitempty"`
	Next       string `json:"next,omitempty"`
}

func InspectKeyword(store *library.Store, sources []library.Source) Health {
	const rebuild = "braindex index rebuild"
	if _, err := os.Stat(filepath.Join(store.MetaDir(), "index.sqlite")); errors.Is(err, os.ErrNotExist) {
		return Health{State: "missing", Next: rebuild}
	}
	idx, err := Open(store, sources)
	if errors.Is(err, ErrStale) {
		return Health{State: "stale", Next: rebuild}
	}
	if err != nil {
		return Health{State: "unavailable", Next: rebuild}
	}
	idx.Close()
	return Health{State: "current"}
}

func InspectEmbeddings(ctx context.Context, store *library.Store, sources []library.Source, probe bool) Health {
	const rebuild = "braindex index embed"
	db, err := openEmbeddingDB(store)
	if errors.Is(err, ErrEmbeddingsMissing) {
		return Health{State: "missing", Next: rebuild}
	}
	if err != nil {
		return Health{State: "unavailable", Next: rebuild}
	}
	defer db.Close()
	settings, err := readEmbeddingSettings(ctx, db)
	if err != nil {
		return Health{State: "stale", Next: rebuild}
	}
	report := Health{State: "current", Model: settings.model, Dimensions: settings.dimension}
	if !settings.current(sources) {
		report.State, report.Next = "stale", rebuild
		return report
	}
	if !probe {
		return report
	}
	e, err := localEmbedder()
	if err != nil {
		report.Runtime = "unavailable"
		return report
	}
	defer e.client.CloseIdleConnections()
	model, err := e.model(ctx, settings.model)
	switch {
	case err != nil:
		report.Runtime = "unavailable"
	case model.digest != settings.digest:
		report.State, report.Runtime, report.Next = "stale", "model_changed", rebuild
	default:
		report.Runtime = "ready"
	}
	return report
}

type embeddingSettings struct {
	version, dimension        int
	generation, model, digest string
	minimum                   float64
}

func openEmbeddingDB(store *library.Store) (*sql.DB, error) {
	file := filepath.Join(store.MetaDir(), embeddingFile)
	if _, err := os.Stat(file); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrEmbeddingsMissing
		}
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn(file, "ro"))
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

func readEmbeddingSettings(ctx context.Context, db *sql.DB) (embeddingSettings, error) {
	var s embeddingSettings
	err := db.QueryRowContext(ctx, "SELECT version, generation, model, digest, dimension, minimum FROM settings").
		Scan(&s.version, &s.generation, &s.model, &s.digest, &s.dimension, &s.minimum)
	return s, err
}

func (s embeddingSettings) current(sources []library.Source) bool {
	return s.version == embeddingVersion && s.dimension >= 0 && s.dimension <= 8192 && s.generation == generation(sources)
}
