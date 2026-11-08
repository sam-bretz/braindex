package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sam-bretz/braindex/internal/library"
)

func TestPodcastRefreshPreservesTranscriptAndMemberships(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/feed" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `<rss version="2.0"><channel><item><guid>episode-1</guid><title>Episode version %d</title><link>/episode</link></item></channel></rss>`, version.Load())
	}))
	defer server.Close()

	root := t.TempDir()
	run := func(args ...string) []byte {
		t.Helper()
		var out, stderr bytes.Buffer
		if code := Run(append([]string{"--library", root}, args...), strings.NewReader(""), &out, &stderr); code != 0 {
			t.Fatalf("braindex %v returned %d: %s", args, code, stderr.String())
		}
		return out.Bytes()
	}
	run("init")
	run("brain", "create", "show")
	run("brain", "create", "guest")
	episode := func(output []byte) library.Source {
		t.Helper()
		var records []library.Source
		if err := json.Unmarshal(output, &records); err != nil || len(records) != 1 {
			t.Fatalf("expected one discovered episode, got %s (%v)", output, err)
		}
		return records[0]
	}
	initial := episode(run("discover", "podcast", server.URL+"/feed", "--brain", "show"))
	if initial.Status != "unavailable" || initial.URL != server.URL+"/episode" {
		t.Fatalf("expected metadata-only discovery, got %+v", initial)
	}
	transcript := filepath.Join(root, "episode.vtt")
	if err := os.WriteFile(transcript, []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<v Guest>Ask about a recent example.</v>\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("source", "transcript", initial.ID, transcript)
	version.Store(2)
	refreshed := episode(run("discover", "podcast", server.URL+"/feed", "--brain", "guest"))
	if refreshed.ID != initial.ID || refreshed.Status != "ready" || refreshed.Title != "Episode version 2" {
		t.Fatalf("refresh lost source identity, text, or metadata: %+v", refreshed)
	}

	store, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Source(initial.ID)
	if err != nil || len(source.Passages) != 1 || source.Passages[0].Text != "Ask about a recent example." ||
		source.Passages[0].Locator != "00:00:01.000–00:00:02.000 (Guest)" {
		t.Fatalf("attached transcript = %+v, %v", source, err)
	}
	brains, err := store.Brains()
	if err != nil || len(brains) != 2 {
		t.Fatalf("brains = %+v, %v", brains, err)
	}
	for _, brain := range brains {
		if !slices.Equal(brain.Sources, []string{source.ID}) {
			t.Fatalf("unexpected memberships for %+v", brain)
		}
	}
}
