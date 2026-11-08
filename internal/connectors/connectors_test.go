package connectors

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sam-bretz/braindex/internal/library"
)

func TestPublicConnectors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/article":
			fmt.Fprint(w, `<!doctype html><html><head><title>Research notes</title></head><body><h1>Research</h1><p>Look for recent evidence.</p><script>should not be indexed</script></body></html>`)
		case "/feed":
			fmt.Fprint(w, `<rss version="2.0"><channel><title>Feed</title><item><guid>1</guid><title>Research</title><link>/article</link><description><![CDATA[<p>Look for recent evidence.</p>]]></description></item><item><guid>2</guid><title>Empty episode</title></item></channel></rss>`)
		case "/atom":
			fmt.Fprint(w, `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>entry-1</id><title>Episode</title><author><name>Speaker</name></author><link rel="enclosure" href="/audio.mp3"/><summary>Summary</summary></entry></feed>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx := context.Background()
	source, err := FetchWeb(ctx, server.URL+"/article#heading")
	if err != nil || source.Title != "Research notes" || source.URL != server.URL+"/article" ||
		source.Status != "ready" || strings.Contains(fmt.Sprint(source.Passages), "should not") {
		t.Fatalf("article %+v, %v", source, err)
	}
	rss, err := FetchFeed(ctx, server.URL+"/feed")
	if err != nil || len(rss) != 2 || rss[0].Status != "ready" || rss[1].Status != "unavailable" {
		t.Fatalf("feed %+v, %v", rss, err)
	}
	again, err := FetchFeed(ctx, server.URL+"/feed")
	if err != nil || again[0].ID != rss[0].ID {
		t.Fatalf("unstable feed identity: %+v, %v", again, err)
	}
	podcast, err := FetchPodcast(ctx, server.URL+"/atom")
	if err != nil || len(podcast) != 1 || podcast[0].Status != "unavailable" || podcast[0].Author != "Speaker" ||
		podcast[0].URL != server.URL+"/audio.mp3" || len(podcast[0].Passages) != 0 {
		t.Fatalf("podcast %+v, %v", podcast, err)
	}
}

func TestYouTubeDiscoveryDoesNotInventTranscript(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Goog-Api-Key") != "fixture-key" || r.URL.Query().Get("playlistId") != "fixture-playlist" {
			t.Error("expected API key and requested playlist")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("pageToken") == "" {
			fmt.Fprint(w, `{"nextPageToken":"second","items":[{"id":"entry-1","snippet":{"title":"Episode","videoOwnerChannelTitle":"Channel"},"contentDetails":{"videoId":"video-1","videoPublishedAt":"2026-01-02T03:04:05Z"}}]}`)
			return
		}
		fmt.Fprint(w, `{"items":[{"id":"entry-2","snippet":{"title":"Duplicate"},"contentDetails":{"videoId":"video-1"}},{"id":"entry-3","snippet":{"title":"Private video"}}]}`)
	}))
	defer server.Close()
	sources, err := fetchPlaylist(context.Background(), server.URL, "fixture-playlist", "fixture-key")
	if err != nil || len(sources) != 2 || sources[0].Title != "Episode" || sources[0].Author != "Channel" ||
		sources[0].Date != "2026-01-02T03:04:05Z" || sources[0].Status != "unavailable" || len(sources[0].Passages) != 0 ||
		sources[0].URL != "https://youtu.be/video-1" || sources[1].Title != "Private video" {
		t.Fatalf("playlist %+v, %v", sources, err)
	}
}

func TestGmailSyncOnlySavesRequestedLabelAndRetriesFailures(t *testing.T) {
	const label = "Label_42"
	var fail atomic.Bool
	fail.Store(true)
	var lock sync.Mutex
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/messages":
			if r.URL.Query().Get("labelIds") != label {
				t.Error("Gmail request was not scoped to the selected label")
			}
			fmt.Fprint(w, `{"messages":[{"id":"first"},{"id":"second"},{"id":"removed"}]}`)
		case "/messages/first", "/messages/second", "/messages/removed":
			id := strings.TrimPrefix(r.URL.Path, "/messages/")
			lock.Lock()
			seen[id]++
			lock.Unlock()
			if id == "second" && fail.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			messageLabel := label
			if id == "removed" {
				messageLabel = "OTHER"
			}
			raw := base64.RawURLEncoding.EncodeToString([]byte("From: Analyst <analyst@example.test>\r\nSubject: " + id + "\r\n\r\nIssue text"))
			fmt.Fprintf(w, `{"labelIds":[%q],"raw":%q}`, messageLabel, raw)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	saved := map[string]string{}
	already := func(id string) bool { return saved[id] != "" }
	save := func(s library.Source) error {
		if s.Path != "" || s.Status != "ready" || len(s.Passages) != 1 {
			t.Fatalf("unexpected Gmail source %+v", s)
		}
		saved[s.ID] = s.Title
		return nil
	}
	ctx := context.Background()
	n, err := syncGmail(ctx, server.URL+"/messages", label, "fixture-token", already, save)
	if n != 1 || err == nil || !strings.Contains(err.Error(), "second") {
		t.Fatalf("first sync saved %d, %v", n, err)
	}
	fail.Store(false)
	n, err = syncGmail(ctx, server.URL+"/messages", label, "fixture-token", already, save)
	lock.Lock()
	defer lock.Unlock()
	if n != 1 || err != nil || len(saved) != 2 || seen["first"] != 1 || seen["second"] != 2 {
		t.Fatalf("retry saved %d, %v, all=%v, fetched=%v", n, err, saved, seen)
	}
}

func TestCredentialedConnectorDoesNotFollowRedirect(t *testing.T) {
	var reached atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	_, _, err := get(context.Background(), origin.URL, map[string]string{"X-Goog-Api-Key": "fixture-key"}, maxPublicResponse)
	if err == nil || reached.Load() {
		t.Fatalf("credentialed request followed a redirect: reached=%t, error=%v", reached.Load(), err)
	}
}

func TestFetchErrorOmitsURLQuery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := FetchWeb(ctx, "https://example.test/resource?token=fixture-secret")
	if err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("expected an error that hides the URL query, got %v", err)
	}
}

func TestRefreshGmailToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.RawQuery != "" {
			t.Error("expected credentials in a POST body")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != "client" ||
			r.Form.Get("client_secret") != "fixture-client-secret" || r.Form.Get("refresh_token") != "fixture-refresh-token" {
			t.Error("expected OAuth refresh credentials")
		}
		fmt.Fprint(w, `{"access_token":"new-token","token_type":"Bearer"}`)
	}))
	defer server.Close()
	token, err := refreshGmailToken(context.Background(), server.URL, "client", "fixture-client-secret", "fixture-refresh-token")
	if err != nil || token != "new-token" {
		t.Fatalf("OAuth refresh failed: %v", err)
	}
}
