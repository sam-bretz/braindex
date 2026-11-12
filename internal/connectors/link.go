package connectors

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
)

// LinkPreview describes work that can be saved after user review.
type LinkPreview struct {
	Kind, URL, Detail string
	Sources           []library.Source
}

func PreviewLink(ctx context.Context, address string, limit int, youtube YouTubeExtractor, apiKey string) (LinkPreview, error) {
	if limit < 1 || limit > 50 {
		return LinkPreview{}, errors.New("link preview limit must be between 1 and 50")
	}
	parsed, err := webURL(strings.TrimSpace(address))
	if err != nil {
		return LinkPreview{}, err
	}
	if _, err := publicYouTubeURL(parsed.String()); err == nil {
		if id, err := YouTubeVideoID(parsed.String()); err == nil && parsed.Query().Get("list") == "" {
			return LinkPreview{
				Kind: "youtube", URL: parsed.String(), Detail: "Fetch a caption track after saving the video.",
				Sources: []library.Source{{ID: ingest.StableID("youtube", id), Title: "https://youtu.be/" + id, URL: "https://youtu.be/" + id, Status: "unavailable"}},
			}, nil
		}
		if series, err := youtubeSeriesURL(parsed.String()); err == nil {
			var sources []library.Source
			var fetchErr error
			if apiKey != "" && parsed.Query().Get("list") != "" {
				sources, fetchErr = FetchYouTubePlaylist(ctx, series, apiKey)
			} else {
				sources, fetchErr = youtube.Discover(ctx, series, limit)
			}
			if len(sources) == 0 && fetchErr != nil {
				return referencePreview(parsed, fetchErr.Error()), nil
			}
			detail := "Metadata only. Fetch caption tracks separately."
			if fetchErr != nil {
				detail = fetchErr.Error()
			}
			return LinkPreview{Kind: "youtube", URL: parsed.String(), Detail: detail, Sources: sources[:min(limit, len(sources))]}, nil
		}
		return referencePreview(parsed, "This YouTube URL does not identify a video or a series."), nil
	}
	client := publicLinkClient()
	defer client.CloseIdleConnections()
	return previewHTTPLink(ctx, client, parsed, limit), nil
}

func publicLinkClient() *http.Client {
	dialer := net.Dialer{Timeout: 10 * time.Second, Control: checkPublicPeer}
	transport := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) >= 5 || (via[0].URL.Scheme == "https" && next.URL.Scheme != "https") {
			return errors.New("link redirect is unsupported")
		}
		_, err := webURL(next.URL.String())
		return err
	}}
}

func previewHTTPLink(ctx context.Context, client *http.Client, address *url.URL, limit int) LinkPreview {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		return referencePreview(address, "This link could not be requested.")
	}
	response, err := client.Do(req)
	if err != nil {
		return referencePreview(address, "The public link could not be fetched.")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return referencePreview(address, fmt.Sprintf("The link returned HTTP %d.", response.StatusCode))
	}
	kind, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	final := response.Request.URL
	if kind == "application/pdf" {
		return pdfReferencePreview(final)
	}
	const maxLinkBytes = 24 << 20
	if response.ContentLength > maxLinkBytes {
		return referencePreview(address, "The response exceeds 24 MiB.")
	}
	body := bufio.NewReaderSize(response.Body, 512)
	if header, _ := body.Peek(len("%PDF-")); bytes.Equal(header, []byte("%PDF-")) {
		return pdfReferencePreview(final)
	}
	data, err := io.ReadAll(io.LimitReader(body, maxLinkBytes+1))
	if err != nil || len(data) > maxLinkBytes {
		return referencePreview(address, "The response could not be read within the 24 MiB limit.")
	}
	sniffed := ""
	if kind == "" || kind == "application/octet-stream" {
		sniffed = http.DetectContentType(data)
	}
	preview := LinkPreview{URL: final.String()}
	var source library.Source
	switch {
	case strings.Contains(kind, "xml") || strings.HasPrefix(sniffed, "text/xml"):
		entries, err := decodeFeed(data)
		if err != nil {
			return referencePreview(final, "The XML is not a supported RSS or Atom feed.")
		}
		podcast := podcastEntries(entries)
		preview.Kind = "rss"
		if podcast {
			preview.Kind, preview.Detail = "podcast", "Metadata only. Transcribe selected episodes separately."
		}
		preview.Sources, err = feedSources(final, entries[:min(limit, len(entries))], podcast)
		if err != nil {
			return referencePreview(final, "Feed entries could not be extracted.")
		}
		return preview
	case kind == "text/html" || strings.HasPrefix(sniffed, "text/html"):
		source, err = webSource(data, final)
		preview.Kind = "article"
	case kind == "text/plain" || strings.HasPrefix(sniffed, "text/plain"):
		passages := ingest.LinePassages(string(data))
		source = library.Source{ID: ingest.StableID("web", final.String()), URL: final.String(), Title: path.Base(final.Path),
			Status: ingest.TextStatus(passages), Passages: passages}
		preview.Kind = "article"
	default:
		return referencePreview(final, "This format can be saved as a reference.")
	}
	if err != nil || source.Status != "ready" {
		return referencePreview(final, "This page did not yield searchable text.")
	}
	preview.Sources = []library.Source{source}
	return preview
}

// Public PDFs are references. The local parser can expand compressed streams
// before extracted-text limits take effect.
func pdfReferencePreview(address *url.URL) LinkPreview {
	return LinkPreview{
		Kind: "pdf", URL: address.String(),
		Detail: "PDFs from links are saved as references. Use source add with a trusted local PDF to extract text.",
		Sources: []library.Source{{
			ID: ingest.StableID("web", address.String()), URL: address.String(), Title: path.Base(address.Path),
			MIME: "application/pdf", Status: "unavailable",
		}},
	}
}

func referencePreview(address *url.URL, detail string) LinkPreview {
	return LinkPreview{Kind: "reference", URL: address.String(), Detail: detail,
		Sources: []library.Source{{URL: address.String(), Title: address.Hostname(), Status: "unavailable"}}}
}

func podcastEntries(entries []entry) bool {
	for _, entry := range entries {
		if mediaEnclosure(entry.Enclosure.Type, entry.Enclosure.URL) {
			return true
		}
		for _, link := range entry.Links {
			if link.Rel == "enclosure" && mediaEnclosure(link.Type, first(link.Href, link.Text)) {
				return true
			}
		}
	}
	return false
}

func mediaEnclosure(kind, address string) bool {
	if strings.HasPrefix(kind, "audio/") || strings.HasPrefix(kind, "video/") {
		return true
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return false
	}
	switch strings.ToLower(path.Ext(parsed.Path)) {
	case ".mp3", ".m4a", ".mp4", ".ogg", ".opus", ".wav", ".aac", ".flac", ".webm":
		return true
	}
	return false
}
