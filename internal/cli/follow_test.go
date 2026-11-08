package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sam-bretz/braindex/internal/library"
)

func followTestCommand(t *testing.T, root string, codeExpected int, args ...string) []byte {
	t.Helper()
	var out, stderr bytes.Buffer
	code := Run(append([]string{"--library", root}, args...), nil, &out, &stderr)
	if code != codeExpected {
		t.Fatalf("%v returned %d: %s %s", args, code, out.String(), stderr.String())
	}
	return out.Bytes()
}

func followTestReports(t *testing.T, raw []byte) []followRefreshResult {
	t.Helper()
	var report []followRefreshResult
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("decode report: %v (%s)", err, raw)
	}
	return report
}

func TestFollowFeedRefreshDeltasAndIsolatesFailure(t *testing.T) {
	var phase atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		switch phase.Load() {
		case 0:
			fmt.Fprint(w, `<rss><channel><item><guid>one</guid><title>Before</title><link>/one</link><description>Original evidence.</description></item></channel></rss>`)
		default:
			fmt.Fprint(w, `<rss><channel><item><guid>one</guid><title>After</title><link>/one</link><description>Revised evidence.</description></item><item><guid>two</guid><title>Waiting</title><link>/two</link></item></channel></rss>`)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	run := func(code int, args ...string) []byte { return followTestCommand(t, root, code, args...) }
	run(0, "init")
	run(0, "brain", "create", "feed")
	run(0, "rule", "set", "example", "--match", "example topic", "--brain", "feed")
	rules := string(run(0, "rule", "list"))
	run(0, "follow", "add", "valid", server.URL+"/good", "--brain", "feed", "--allow-private")
	run(0, "follow", "add", "failure", server.URL+"/bad", "--brain", "feed", "--allow-private")
	report := followTestReports(t, run(1, "follow", "refresh"))
	if len(report) != 2 || report[0].Status != "failed" || len(report[1].Items) != 1 ||
		report[1].Items[0].Change != "new" || report[1].Items[0].Status != "ready" {
		t.Fatalf("first refresh: %+v", report)
	}
	firstID := report[1].Items[0].ID
	phase.Store(1)
	report = followTestReports(t, run(0, "follow", "refresh", "valid"))
	if len(report) != 1 || len(report[0].Items) != 2 || report[0].Items[0].ID != firstID ||
		report[0].Items[0].Change != "changed" || report[0].Items[1].Change != "new" ||
		report[0].Items[1].Status != "waiting" {
		t.Fatalf("second refresh: %+v", report)
	}
	if actual := string(run(0, "rule", "list")); actual != rules {
		t.Fatalf("refresh changed routing: %s", actual)
	}
	if answer := string(run(0, "context", "revised evidence", "--brain", "feed")); !strings.Contains(answer, "Revised evidence") {
		t.Fatalf("refresh did not index revised evidence: %s", answer)
	}
	report = followTestReports(t, run(0, "follow", "refresh", "valid"))
	if report[0].Items[0].Change != "unchanged" || report[0].Items[1].Change != "unchanged" {
		t.Fatalf("repeated refresh: %+v", report)
	}
}

func TestFollowReviewedAuthorCandidatesStayOutUntilSelected(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	root := t.TempDir()
	run := func(code int, args ...string) []byte { return followTestCommand(t, root, code, args...) }
	run(0, "init")
	run(0, "brain", "create", "sales")
	run(0, "follow", "add", "author", "--author", "Alex Example", "--hint", "founder sales", "--brain", "sales")
	file := filepath.Join(root, "results.json")
	if err := os.WriteFile(file, []byte(`[
		{"kind":"article","title":"Alex Example on founder sales","url":"https://example.org/sales"},
		{"kind":"article","title":"Same page","url":"https://example.org/sales#intro"},
		{"kind":"book","title":"Alex Example on birdwatching","url":"https://example.net/book"}
	]`), 0600); err != nil {
		t.Fatal(err)
	}
	report := followTestReports(t, run(0, "follow", "refresh", "author", "--results", file))
	if len(report[0].Items) != 2 || report[0].Items[0].Status != "review" || report[0].Items[0].Next != "save" {
		t.Fatalf("review candidates = %+v", report)
	}
	store, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := store.Sources()
	if err != nil || len(sources) != 0 {
		t.Fatalf("discovery added unreviewed candidates: %+v, %v", sources, err)
	}
	run(0, "follow", "save", "author", report[0].Items[0].ID)
	run(0, "follow", "save", "author", report[0].Items[0].ID)
	failed := followTestReports(t, run(1, "follow", "refresh", "author", "--search"))
	if failed[0].Status != "failed" {
		t.Fatalf("expected scoped provider failure: %+v", failed)
	}
	status := followTestReports(t, run(0, "follow", "refresh"))
	if status[0].Status != "needs_results" {
		t.Fatalf("scheduled refresh should not send an author query: %+v", status)
	}
	report = followTestReports(t, run(0, "follow", "refresh", "author", "--results", file))
	sources, err = store.Sources()
	if err != nil || len(sources) != 1 || sources[0].URL != "https://example.org/sales" ||
		report[0].Items[0].Status != "saved" || report[0].Items[0].Change != "unchanged" {
		t.Fatalf("follow state = %+v, sources = %+v, error = %v", report, sources, err)
	}
}

func TestFollowDefaultsToPublicAndRejectsUntrackedAdapterArgs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("default follow refresh reached a private address")
	}))
	defer server.Close()
	root := t.TempDir()
	run := func(code int, args ...string) []byte { return followTestCommand(t, root, code, args...) }
	run(0, "init")
	run(0, "brain", "create", "test")
	run(0, "follow", "add", "private", server.URL, "--brain", "test")
	report := followTestReports(t, run(1, "follow", "refresh", "private"))
	if report[0].Status != "failed" {
		t.Fatalf("unexpected refresh result: %+v", report)
	}
	store, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"youtube", "podcast"} {
		follow := library.Follow{ID: kind, Brain: "test", Kind: kind, Items: []library.FollowItem{{ID: kind + "-known"}}}
		if err := store.AddFollow(follow); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		code := Run([]string{"--library", root, "follow", "process", kind, kind + "-known", "--", kind + "-foreign"}, nil, &out, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "cannot contain additional source IDs") {
			t.Fatalf("%s adapter args = %d, %s, %s", kind, code, out.String(), stderr.String())
		}
	}
}

func TestFollowRefreshIndexesAManuallyAttachedTranscript(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<rss><channel><item><guid>one</guid><title>One episode</title><link>/one</link><enclosure url="/one.mp3" type="audio/mpeg"/></item></channel></rss>`)
	}))
	defer server.Close()
	root := t.TempDir()
	run := func(code int, args ...string) []byte { return followTestCommand(t, root, code, args...) }
	run(0, "init")
	run(0, "brain", "create", "show")
	run(0, "follow", "add", "podcast", server.URL, "--kind", "podcast", "--brain", "show", "--allow-private")
	first := followTestReports(t, run(0, "follow", "refresh", "podcast"))
	id := first[0].Items[0].ID
	path := filepath.Join(root, "sample.vtt")
	if err := os.WriteFile(path, []byte("WEBVTT\n\n00:00:04.000 --> 00:00:07.000\nAsk for a recent customer example.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(0, "source", "transcript", id, path)
	refreshed := followTestReports(t, run(0, "follow", "refresh", "podcast"))
	if refreshed[0].Items[0].Change != "became_searchable" || refreshed[0].Items[0].Next != "" {
		t.Fatalf("transcript refresh: %+v", refreshed)
	}
	answer := string(run(0, "context", "recent customer example", "--brain", "show"))
	if !strings.Contains(answer, "00:00:04.000–00:00:07.000") {
		t.Fatalf("refresh did not index the attached transcript: %s", answer)
	}
}

func TestFollowProcessReportsMissingTranscriberPerEpisode(t *testing.T) {
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBrain(library.Brain{ID: "show", Name: "Show"}); err != nil {
		t.Fatal(err)
	}
	ready, err := store.PutSource(library.Source{ID: "podcast-ready", Title: "Ready episode", Passages: []library.Passage{{Text: "A ready episode", Locator: "00:00:01.000–00:00:02.000"}}}, []string{"show"})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := store.PutSource(library.Source{ID: "podcast-waiting", Title: "Waiting episode", AudioURL: "https://example.org/audio.mp3"}, []string{"show"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddFollow(library.Follow{ID: "show", Kind: "podcast", Brain: "show",
		Items: []library.FollowItem{{ID: ready.ID}, {ID: waiting.ID}}}); err != nil {
		t.Fatal(err)
	}
	raw := followTestCommand(t, store.Root, 1, "follow", "process", "show", ready.ID, waiting.ID,
		"--", "--transcriber", filepath.Join(store.Root, "missing-tool"))
	var results []transcriptResult
	if err := json.Unmarshal(raw, &results); err != nil || len(results) != 2 ||
		results[0].Status != "cached" || results[1].Status != "failed" || results[1].Next == "" {
		t.Fatalf("processing outcomes = %+v, %v", results, err)
	}
}
