package connectors

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPodcastAudioDownloadBoundaries(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wrong-content" {
			w.Header().Set("Content-Type", "text/html")
		} else {
			w.Header().Set("Content-Type", "audio/mpeg")
		}
		_, _ = w.Write([]byte("audio"))
	}))
	defer server.Close()
	var output bytes.Buffer
	if err := downloadPodcastAudio(t.Context(), server.Client(), server.URL, &output, 10); err != nil || output.String() != "audio" {
		t.Fatalf("download = %q, %v", output.String(), err)
	}
	for _, scenario := range []struct {
		path  string
		limit int64
	}{{"/wrong-content", 10}, {"/", 2}} {
		if err := downloadPodcastAudio(t.Context(), server.Client(), server.URL+scenario.path, &output, scenario.limit); err == nil {
			t.Fatalf("unexpected success: %+v", scenario)
		}
	}
	for _, address := range []string{"127.0.0.1:443", "192.168.1.2:443", "100.64.0.1:443", "[::ffff:169.254.169.254]:443", "[::1]:443"} {
		if err := checkPublicPeer("tcp", address, nil); err == nil {
			t.Errorf("private destination %s accepted", address)
		}
	}
	if err := checkPublicPeer("tcp", "1.1.1.1:443", nil); err != nil {
		t.Errorf("public destination rejected: %v", err)
	}
}

func TestPodcastFeedKeepsPageAndAudioURLs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss version="2.0"><channel><item>
			<title>An episode</title><guid>episode-1</guid><link>/episode</link>
			<enclosure url="/audio.mp3" type="audio/mpeg"/>
		</item></channel></rss>`))
	}))
	defer server.Close()
	sources, err := FetchPodcast(t.Context(), server.URL)
	if err != nil || len(sources) != 1 || sources[0].URL != server.URL+"/episode" || sources[0].AudioURL != server.URL+"/audio.mp3" || sources[0].Status != "unavailable" {
		t.Fatalf("sources = %+v, %v", sources, err)
	}
}
