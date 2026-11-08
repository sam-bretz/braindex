package connectors

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
	"golang.org/x/net/html/charset"
)

// FetchWeb retrieves visible article text without executing scripts.
func FetchWeb(ctx context.Context, address string) (library.Source, error) {
	data, final, err := get(ctx, address, nil, maxPublicResponse)
	if err != nil {
		return library.Source{}, err
	}
	return webSource(data, final)
}

// FetchPublicWeb retrieves article text with public-only connections, including redirects.
func FetchPublicWeb(ctx context.Context, address string) (library.Source, error) {
	client := publicLinkClient()
	defer client.CloseIdleConnections()
	data, final, err := requestWithClient(ctx, client, http.MethodGet, address, nil, nil, maxPublicResponse)
	if err != nil {
		return library.Source{}, err
	}
	return webSource(data, final)
}

func webSource(data []byte, final *url.URL) (library.Source, error) {
	title, body, err := ingest.HTMLText(bytes.NewReader(data))
	if err != nil {
		return library.Source{}, err
	}
	if title == "" {
		title = final.Host
	}
	source := library.Source{ID: ingest.StableID("web", final.String()), URL: final.String(), Title: title}
	source.Passages = ingest.LinePassages(body)
	for i := range source.Passages {
		source.Passages[i].Locator = "extracted page " + source.Passages[i].Locator
	}
	source.Status = ingest.TextStatus(source.Passages)
	return source, nil
}

type feedLink struct {
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
	Href string `xml:"href,attr"`
	Text string `xml:",chardata"`
}

type feedAuthor struct {
	Name string `xml:"name"`
	Text string `xml:",chardata"`
}

type entry struct {
	Title       string     `xml:"title"`
	ID          string     `xml:"id"`
	GUID        string     `xml:"guid"`
	Links       []feedLink `xml:"link"`
	Author      feedAuthor `xml:"author"`
	Creator     string     `xml:"creator"`
	Published   string     `xml:"published"`
	Updated     string     `xml:"updated"`
	PubDate     string     `xml:"pubDate"`
	Content     string     `xml:"content"`
	Encoded     string     `xml:"encoded"`
	Description string     `xml:"description"`
	Summary     string     `xml:"summary"`
	Enclosure   struct {
		URL  string `xml:"url,attr"`
		Type string `xml:"type,attr"`
	} `xml:"enclosure"`
}

type feed struct {
	XMLName xml.Name
	Entries []entry `xml:"entry"`
	Channel struct {
		Items []entry `xml:"item"`
	} `xml:"channel"`
}

func first(values ...string) string {
	for _, value := range values {
		if clean := strings.TrimSpace(value); clean != "" {
			return clean
		}
	}
	return ""
}

// FetchFeed reads RSS and Atom entries and indexes text included by the feed.
func FetchFeed(ctx context.Context, address string) ([]library.Source, error) {
	return fetchFeed(ctx, address, false)
}

// FetchPodcast discovers podcast episodes and leaves transcripts unavailable.
func FetchPodcast(ctx context.Context, address string) ([]library.Source, error) {
	return fetchFeed(ctx, address, true)
}

// FetchFollowFeed uses a public-only transport by default because a saved
// feed may be refreshed repeatedly without a user inspecting its redirects.
func FetchFollowFeed(ctx context.Context, address string, podcast, allowPrivate bool) ([]library.Source, error) {
	if allowPrivate {
		return fetchFeed(ctx, address, podcast)
	}
	parsed, err := webURL(address)
	if err != nil {
		return nil, err
	}
	client := publicLinkClient()
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not fetch the public feed from %s", parsed.Hostname())
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("feed returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxPublicResponse+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPublicResponse {
		return nil, fmt.Errorf("feed exceeds %d bytes", maxPublicResponse)
	}
	entries, err := decodeFeed(data)
	if err != nil {
		return nil, err
	}
	return feedSources(response.Request.URL, entries, podcast)
}

func fetchFeed(ctx context.Context, address string, podcast bool) ([]library.Source, error) {
	data, base, err := get(ctx, address, nil, maxPublicResponse)
	if err != nil {
		return nil, err
	}
	entries, err := decodeFeed(data)
	if err != nil {
		return nil, err
	}
	return feedSources(base, entries, podcast)
}

func decodeFeed(data []byte) ([]entry, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.CharsetReader = charset.NewReaderLabel
	var parsed feed
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("read feed XML: %w", err)
	}
	var entries []entry
	switch parsed.XMLName.Local {
	case "rss":
		entries = parsed.Channel.Items
	case "feed":
		entries = parsed.Entries
	default:
		return nil, fmt.Errorf("expected an RSS or Atom feed")
	}
	return entries, nil
}

func feedSources(base *url.URL, entries []entry, podcast bool) ([]library.Source, error) {
	sources := make([]library.Source, 0, len(entries))
	for _, item := range entries {
		source, err := feedSource(base, item, podcast)
		if err != nil {
			return sources, err
		}
		if source.ID != "" {
			sources = append(sources, source)
		}
	}
	return sources, nil
}

func feedSource(base *url.URL, item entry, podcast bool) (library.Source, error) {
	var article, enclosure string
	for _, itemLink := range item.Links {
		address := link(base, first(itemLink.Href, itemLink.Text))
		if article == "" && (itemLink.Rel == "" || itemLink.Rel == "alternate") {
			article = address
		}
		if itemLink.Rel == "enclosure" && enclosure == "" {
			enclosure = address
		}
	}
	media := first(enclosure, link(base, item.Enclosure.URL))
	address := first(article, media)
	key := first(item.GUID, item.ID, address)
	if key == "" {
		return library.Source{}, nil
	}
	kind := "feed"
	if podcast {
		kind = "podcast"
	}
	source := library.Source{
		ID: ingest.StableID(kind, base.String()+"\n"+key), URL: address,
		Title: first(item.Title, address, key), Author: first(item.Author.Name, item.Author.Text, item.Creator),
		Date: first(item.Published, item.PubDate, item.Updated), Status: "unavailable",
	}
	if podcast {
		source.AudioURL = media
		return source, nil
	}
	content := first(item.Encoded, item.Content, item.Description, item.Summary)
	_, clean, err := ingest.HTMLText(strings.NewReader(content))
	if err != nil {
		return library.Source{}, err
	}
	source.Passages = ingest.LinePassages(clean)
	for i := range source.Passages {
		source.Passages[i].Locator = "feed entry " + source.Passages[i].Locator
	}
	source.Status = ingest.TextStatus(source.Passages)
	return source, nil
}
