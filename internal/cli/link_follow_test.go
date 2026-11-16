package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sam-bretz/braindex/internal/library"
	"github.com/sam-bretz/braindex/internal/retrieval"
)

func TestLinkFollowFromSeriesToSelectedCitations(t *testing.T) {
	const channel = "https://www.youtube.com/@daltonplusmichael/videos"
	const brain = "dalton-michael"
	const follow = "dalton-michael-channel"
	const firstURL = "https://youtu.be/Fixture001A"
	const secondURL = "https://youtu.be/Fixture002B"
	const thirdURL = "https://youtu.be/Fixture003C"
	const fourthURL = "https://youtu.be/Fixture004D"
	const locator = "00:02:10.000–00:02:28.000"
	fixtures := t.TempDir()
	logPath := filepath.Join(fixtures, "calls")
	extractor := filepath.Join(fixtures, "yt-dlp")
	script := `#!/bin/sh
set -eu
mode=caption
for arg do
  if [ "$arg" = "--dump-single-json" ]; then mode=discovery; fi
  last=$arg
done
printf '%s %s\n' "$mode" "$last" >> "$BRAINDEX_LINK_FOLLOW_TEST_LOG"
if [ "$mode" = discovery ]; then
  [ "$last" = "https://www.youtube.com/@daltonplusmichael/videos" ]
  cat <<'JSON'
{"channel":"Dalton + Michael acceptance fixture","entries":[{"id":"Fixture001A","title":"Qualifying early customers"},{"id":"Fixture002B","title":"A video left unselected"},{"id":"Fixture003C","title":"When to hire"},{"id":"Fixture004D","title":"Founder energy"}]}
JSON
  exit
fi
case "$last" in
  https://youtu.be/Fixture001A) body='Before promising a pilot, check which buyer can approve the budget.' ;;
  https://youtu.be/Fixture003C) body='Set expectations when recruiting the first engineer.' ;;
  https://youtu.be/Fixture004D) body='Keep room for a regular break during the week.' ;;
  *) exit 21 ;;
esac
while [ "$1" != "--output" ]; do shift; done
printf 'WEBVTT\n\n00:02:10.000 --> 00:02:28.000\n%s\n' "$body" > "${2%.*}.en.vtt"
`
	if err := os.WriteFile(extractor, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRAINDEX_YT_DLP", extractor)
	t.Setenv("BRAINDEX_YT_DLP_JS_RUNTIME", "")
	t.Setenv("BRAINDEX_YOUTUBE_API_KEY", "")
	t.Setenv("BRAINDEX_LINK_FOLLOW_TEST_LOG", logPath)
	root := filepath.Join(fixtures, "library")

	run := func(input string, args ...string) []byte {
		t.Helper()
		var out, stderr bytes.Buffer
		command := append([]string{"--library", root}, args...)
		if code := Run(command, strings.NewReader(input), &out, &stderr); code != 0 {
			t.Fatalf("braindex %v exited %d: %s; stdout: %s", args, code, stderr.String(), out.String())
		}
		return out.Bytes()
	}
	decode := func(output []byte, value any) {
		t.Helper()
		if err := json.Unmarshal(output, value); err != nil {
			t.Fatalf("decode %s: %v", output, err)
		}
	}
	captionCalls := func() []string {
		t.Helper()
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		var calls []string
		for _, line := range strings.Split(string(data), "\n") {
			if address, ok := strings.CutPrefix(line, "caption "); ok {
				calls = append(calls, address)
			}
		}
		return calls
	}

	run("", "init")
	var preview linkReview
	decode(run("", "link", channel, "--json"), &preview)
	if preview.Kind != "youtube" || len(preview.Items) != 4 || len(captionCalls()) != 0 {
		t.Fatalf("preview unexpectedly saved or fetched captions: %+v", preview)
	}
	for _, item := range preview.Items {
		if item.Status != "unavailable" {
			t.Fatalf("series item should begin as metadata: %+v", item)
		}
	}

	var sources []library.Source
	decode(run("", "source", "list"), &sources)
	if len(sources) != 0 {
		t.Fatalf("preview saved sources: %+v", sources)
	}
	run("1,3,4\n", "link", channel, "--new-brain", brain)
	decode(run("", "source", "list"), &sources)
	var savedURLs []string
	for _, source := range sources {
		savedURLs = append(savedURLs, source.URL)
	}
	slices.Sort(savedURLs)
	if !slices.Equal(savedURLs, []string{firstURL, thirdURL, fourthURL}) || len(captionCalls()) != 0 {
		t.Fatalf("link selection saved %v and fetched %v", savedURLs, captionCalls())
	}

	run("", "follow", "add", follow, channel, "--brain", brain, "--kind", "youtube", "--yt-dlp")
	var refresh []followRefreshResult
	decode(run("", "follow", "refresh", follow), &refresh)
	if len(refresh) != 1 || len(refresh[0].Items) != 4 || len(captionCalls()) != 0 {
		t.Fatalf("refresh result = %+v, captions = %v", refresh, captionCalls())
	}
	for _, item := range refresh[0].Items {
		if item.Status != "waiting" || item.Change != "new" || item.Next != "process" {
			t.Fatalf("unexpected initial refresh item: %+v", item)
		}
	}

	process := []string{"follow", "process", follow, preview.Items[0].ID, preview.Items[2].ID, preview.Items[3].ID, "--", "--lang", "en"}
	var results []youtubeCaptionResult
	decode(run("", process...), &results)
	if len(results) != 3 {
		t.Fatalf("processed %+v", results)
	}
	for i, result := range results {
		if result.SourceID != process[i+3] || result.Status != "searchable" || result.Cached || result.Revision == "" {
			t.Fatalf("unexpected process result: %+v", result)
		}
	}
	expected := []string{firstURL, thirdURL, fourthURL}
	if calls := captionCalls(); !slices.Equal(calls, expected) {
		t.Fatalf("caption downloads = %v, want %v", calls, expected)
	}

	var result retrieval.Result
	decode(run("", "context", "Who can approve the pilot budget?", "--brain", brain, "--mode", "keyword", "--limit", "1"), &result)
	if len(result.Brains) != 1 || len(result.Brains[0].Excerpts) != 1 {
		t.Fatalf("missing cited evidence: %+v", result)
	}
	citation := result.Brains[0].Excerpts[0]
	if citation.URL != firstURL || citation.Locator != locator || citation.Revision != results[0].Revision ||
		!strings.Contains(citation.Text, "which buyer can approve the budget") {
		t.Fatalf("wrong citation: %+v", citation)
	}

	var refreshed []followRefreshResult
	decode(run("", "follow", "refresh", follow), &refreshed)
	if len(refreshed) != 1 || len(refreshed[0].Items) != 4 {
		t.Fatalf("unexpected refresh after processing: %+v", refreshed)
	}
	for _, item := range refreshed[0].Items {
		if item.URL == secondURL {
			if item.Status != "waiting" || item.Change != "unchanged" || item.Next != "process" {
				t.Fatalf("unselected video should still be waiting: %+v", item)
			}
		} else if item.Status != "ready" || item.Change != "became_searchable" || item.Next != "" {
			t.Fatalf("selected video should be ready: %+v", item)
		}
	}
	var repeated []youtubeCaptionResult
	decode(run("", process...), &repeated)
	if len(repeated) != len(results) {
		t.Fatalf("repeat returned %d results, want %d", len(repeated), len(results))
	}
	for i, result := range repeated {
		if !result.Cached || result.Status != "searchable" || result.Revision != results[i].Revision {
			t.Fatalf("repeat did not reuse the caption revision: %+v", result)
		}
	}
	if calls := captionCalls(); !slices.Equal(calls, expected) {
		t.Fatalf("refresh or repeat fetched extra captions: %v", calls)
	}
}
