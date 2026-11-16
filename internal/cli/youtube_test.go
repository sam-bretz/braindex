package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
)

func TestYouTubeExtractorDiscoveryCaptionsAndCache(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "invocations")
	fake := filepath.Join(root, "yt-dlp")
	script := `#!/bin/sh
set -eu
mode=caption
for arg do
  if [ "$arg" = "--dump-single-json" ]; then mode=discovery; fi
  last=$arg
done
printf '%s %s\n' "$mode" "$last" >> "$YT_TEST_LOG"
if [ "$mode" = discovery ]; then
  case "$last" in
    https://www.youtube.com/@fixture/videos|https://www.youtube.com/playlist?list=PLFixture) ;;
    *) exit 18 ;;
  esac
  cat <<'JSON'
{"channel":"Fixture Channel","entries":[{"id":"VideoReady1","title":"Ready","upload_date":"20260102"},{"id":"VideoReady1","title":"Duplicate"},null,{"id":"VideoEmpty1","title":"No captions"},{"id":"VideoLimit1","title":"Rate limited"},{"id":"VideoLater1","title":"Later"}]}
JSON
  exit
fi
case "$last" in
  https://youtu.be/VideoReady1)
    while [ "$1" != "--output" ]; do shift; done
    output=${2%.*}.en.vtt
    cat > "$output" <<'VTT'
WEBVTT

00:00:01.000 --> 00:00:02.000
A searchable fixture.
VTT
    ;;
  https://youtu.be/VideoEmpty1) ;;
  https://youtu.be/VideoLimit1)
    printf 'ERROR: Unable to download subtitles: HTTP Error 429: Too Many Requests\n' >&2
    exit 1 ;;
  *) exit 19 ;;
esac
`
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRAINDEX_YT_DLP", fake)
	t.Setenv("BRAINDEX_YT_DLP_JS_RUNTIME", "")
	t.Setenv("YT_TEST_LOG", logPath)
	t.Setenv("BRAINDEX_YOUTUBE_API_KEY", "")
	run := func(wantCode int, args ...string) []byte {
		t.Helper()
		var out, stderr bytes.Buffer
		if code := Run(append([]string{"--library", root}, args...), strings.NewReader(""), &out, &stderr); code != wantCode {
			t.Fatalf("braindex %v returned %d, want %d: %s", args, code, wantCode, stderr.String())
		}
		return out.Bytes()
	}

	run(0, "init")
	// A configured executable does not switch the existing default connector.
	run(1, "discover", "youtube", "PLFixture", "--brain", "show", "--create-brain")
	run(1, "discover", "youtube", "https://example.org/playlist?list=PLFixture", "--brain", "show", "--create-brain", "--yt-dlp")
	var sources []library.Source
	if err := json.Unmarshal(run(0, "discover", "youtube", "https://youtube.com/@fixture", "--brain", "show", "--create-brain", "--yt-dlp", "--limit", "4"), &sources); err != nil || len(sources) != 4 {
		t.Fatalf("discovered sources = %+v, %v", sources, err)
	}
	first := sources[0]
	if first.ID != ingest.StableID("youtube", "VideoReady1") || first.Status != "unavailable" ||
		first.URL != "https://youtu.be/VideoReady1" || first.Author != "Fixture Channel" || first.Date != "2026-01-02" {
		t.Fatalf("discovered source = %+v", first)
	}
	var results []youtubeCaptionResult
	if err := json.Unmarshal(run(1, "source", "youtube-captions", first.ID, sources[1].ID, sources[2].ID, sources[3].ID, first.ID), &results); err != nil {
		t.Fatal(err)
	}
	wantStatus := []string{"searchable", "unavailable", "rate_limited", "rate_limited"}
	var gotStatus []string
	for _, result := range results {
		gotStatus = append(gotStatus, result.Status)
	}
	if !reflect.DeepEqual(gotStatus, wantStatus) || results[0].Cached {
		t.Fatalf("caption results = %+v", results)
	}

	var refreshed []library.Source
	if err := json.Unmarshal(run(0, "discover", "youtube", "PLFixture", "--brain", "show", "--yt-dlp"), &refreshed); err != nil || len(refreshed) != 4 ||
		refreshed[0].Status != "ready" || refreshed[0].ID != first.ID {
		t.Fatalf("refresh lost downloaded caption: %+v, %v", refreshed, err)
	}
	if err := json.Unmarshal(run(0, "source", "youtube-captions", first.ID), &results); err != nil || len(results) != 1 ||
		!results[0].Cached || results[0].Revision != refreshed[0].Revision {
		t.Fatalf("cached result = %+v, %v", results, err)
	}
	store, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Source(first.ID)
	if err != nil || len(saved.Passages) != 1 || saved.Passages[0].Text != "A searchable fixture." || !strings.HasSuffix(saved.Path, first.ID+"/en.vtt") {
		t.Fatalf("attached caption = %+v, %v", saved, err)
	}
	run(0, "index", "rebuild")
	if out := run(0, "context", "searchable fixture", "--brain", "show"); !bytes.Contains(out, []byte("00:00:01.000")) || !bytes.Contains(out, []byte(first.URL)) {
		t.Fatalf("missing citation in %s", out)
	}
	invocations, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := "discovery https://www.youtube.com/@fixture/videos\ncaption https://youtu.be/VideoReady1\ncaption https://youtu.be/VideoEmpty1\ncaption https://youtu.be/VideoLimit1\ndiscovery https://www.youtube.com/playlist?list=PLFixture\n"
	if string(invocations) != wantCalls {
		t.Fatalf("extractor calls = %q, want %q", invocations, wantCalls)
	}
}
