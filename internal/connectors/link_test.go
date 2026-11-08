package connectors

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestLinkPreviewClassifiesPublicPayloads(t *testing.T) {
	pdf, err := os.ReadFile("../ingest/testdata/two-pages.pdf")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/article", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<title>Public article</title><p>Evidence belongs here.</p>")
	})
	mux.HandleFunc("/paper", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdf)
	})
	mux.HandleFunc("/podcast", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = io.WriteString(w, `<rss><channel><item><title>Episode one</title><guid>one</guid><link>/one</link><enclosure url="/audio" type="audio/mpeg"/></item><item><title>Episode two</title><guid>two</guid></item></channel></rss>`)
	})
	mux.HandleFunc("/oversized", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "30000000")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/unsupported", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"not":"an article"}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, scenario := range []struct {
		path, kind, locator string
	}{
		{"/article", "article", "extracted page line"},
		{"/paper", "pdf", "page 1"},
		{"/podcast", "podcast", ""},
		{"/missing", "reference", ""},
		{"/oversized", "reference", ""},
		{"/unsupported", "reference", ""},
	} {
		t.Run(scenario.path, func(t *testing.T) {
			address, err := url.Parse(server.URL + scenario.path)
			if err != nil {
				t.Fatal(err)
			}
			preview := previewHTTPLink(t.Context(), server.Client(), address, 1)
			if preview.Kind != scenario.kind || len(preview.Sources) != 1 {
				t.Fatalf("unexpected preview: %+v", preview)
			}
			source := preview.Sources[0]
			if scenario.kind == "pdf" {
				if source.Status != "needs_extraction" || len(source.Passages) != 0 {
					t.Fatalf("preview extracted a PDF before selection: %+v", source)
				}
				source, err = preview.SelectedSource(0)
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario.locator != "" && (source.URL != address.String() || len(source.Passages) == 0 ||
				!strings.HasPrefix(source.Passages[0].Locator, scenario.locator)) {
				t.Fatalf("unexpected source: %+v", source)
			}
			if scenario.kind == "podcast" && (source.Status != "unavailable" || source.AudioURL != server.URL+"/audio") {
				t.Fatalf("unexpected podcast source: %+v", source)
			}
		})
	}
}

func TestLinkPreviewRejectsPrivateNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("preview reached a private destination")
	}))
	defer server.Close()
	preview, err := PreviewLink(t.Context(), server.URL, 1, YouTubeExtractor{}, "")
	if err != nil || preview.Kind != "reference" || preview.Detail == "" {
		t.Fatalf("private network preview: %+v, %v", preview, err)
	}
}
