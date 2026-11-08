package connectors

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
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
		URL string `xml:"url,attr"`
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

func fetchFeed(ctx context.Context, address string, podcast bool) ([]library.Source, error) {
	data, base, err := get(ctx, address, nil, maxPublicResponse)
	if err != nil {
		return nil, err
	}
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
	address := first(article, enclosure, link(base, item.Enclosure.URL))
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
