package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sam-bretz/braindex/internal/library"
)

func TestTranscribeSelectedPodcastAndPreserveRefresh(t *testing.T) {
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBrain(library.Brain{ID: "show", Name: "Show"}); err != nil {
		t.Fatal(err)
	}
	const id = "podcast-episode"
	discovered := library.Source{ID: id, Title: "Original", URL: "https://example.org/episode", AudioURL: "https://example.org/audio.mp3"}
	if _, err := store.PutDiscoveredSource(discovered, []string{"show"}, true); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "transcriber")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
set -eu
test "$(cat "$1")" = 'fixture audio'
printf 'WEBVTT\n\n00:00:02.000 --> 00:00:09.000\nFounders learn from setbacks.\n' > "$2"
`), 0700); err != nil {
		t.Fatal(err)
	}
	var calls int
	download := func(_ context.Context, address string, dst io.Writer) error {
		calls++
		if address != discovered.AudioURL {
			t.Fatalf("download URL = %q", address)
		}
		_, err := io.WriteString(dst, "fixture audio")
		return err
	}
	run := func(args ...string) ([]transcriptResult, error) {
		t.Helper()
		var out bytes.Buffer
		err := (command{out: &out}).transcribePodcast(store, args, download)
		var results []transcriptResult
		if decodeErr := json.Unmarshal(out.Bytes(), &results); decodeErr != nil {
			t.Fatalf("transcribe output = %q, err = %v, decode = %v", out.String(), err, decodeErr)
		}
		return results, err
	}
	results, err := run("--transcriber", script, "podcast-missing", id, id)
	if err == nil || len(results) != 2 || results[0].Status != "failed" || results[1].Status != "searchable" || calls != 1 {
		t.Fatalf("results = %+v, calls = %d, error = %v", results, calls, err)
	}
	transcript, err := store.Source(id)
	if err != nil {
		t.Fatal(err)
	}
	if transcript.Path == "" || len(transcript.Passages) != 1 || transcript.Passages[0].Locator != "00:00:02.000–00:00:09.000" {
		t.Fatalf("transcript = %+v", transcript)
	}
	entries, err := os.ReadDir(filepath.Dir(filepath.Join(store.Root, transcript.Path)))
	if err != nil || len(entries) != 1 || filepath.Ext(entries[0].Name()) != ".vtt" {
		t.Fatalf("durable files = %v, %v", entries, err)
	}
	discovered.Title = "Refreshed title"
	refreshed, err := store.PutDiscoveredSource(discovered, []string{"show"}, true)
	if err != nil || refreshed.Title != "Refreshed title" || refreshed.Path != transcript.Path || refreshed.Status != "ready" {
		t.Fatalf("refresh = %+v, %v", refreshed, err)
	}
	results, err = run("--transcriber", script, id)
	if err != nil || len(results) != 1 || results[0].Status != "cached" || calls != 1 {
		t.Fatalf("cached results = %+v, calls = %d, error = %v", results, calls, err)
	}
	results, err = run("--transcriber", script, "--arg={input}", "--arg={dir}", "--force", id)
	if err == nil || len(results) != 1 || results[0].Status != "failed" || calls != 2 {
		t.Fatalf("failed replacement = %+v, calls = %d, error = %v", results, calls, err)
	}
	final, err := store.Source(id)
	if err != nil || final.Revision != refreshed.Revision {
		t.Fatalf("failed replacement changed revision: %+v, %v", final, err)
	}
}
