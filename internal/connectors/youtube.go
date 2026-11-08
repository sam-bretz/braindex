package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"

	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
)

var providerID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// FetchYouTubePlaylist discovers video metadata through the YouTube Data API.
// A playlist URL or bare playlist ID is accepted.
func FetchYouTubePlaylist(ctx context.Context, playlist, apiKey string) ([]library.Source, error) {
	if parsed, err := url.Parse(playlist); err == nil && parsed.IsAbs() {
		switch parsed.Hostname() {
		case "www.youtube.com", "youtube.com", "m.youtube.com", "youtu.be":
			playlist = parsed.Query().Get("list")
		default:
			return nil, fmt.Errorf("expected a YouTube playlist URL")
		}
	}
	if !providerID.MatchString(playlist) || apiKey == "" {
		return nil, fmt.Errorf("YouTube playlist ID and API key are required")
	}
	return fetchPlaylist(ctx, "https://www.googleapis.com/youtube/v3/playlistItems", playlist, apiKey)
}

func fetchPlaylist(ctx context.Context, endpoint, playlist, apiKey string) ([]library.Source, error) {
	query := url.Values{"part": {"snippet,contentDetails"}, "playlistId": {playlist}, "maxResults": {"50"}}
	visited := map[string]bool{}
	videoIDs := map[string]bool{}
	var sources []library.Source
	for {
		token := query.Get("pageToken")
		if visited[token] {
			return sources, fmt.Errorf("YouTube returned a repeated page token")
		}
		visited[token] = true
		data, _, err := get(ctx, endpoint+"?"+query.Encode(), map[string]string{"X-Goog-Api-Key": apiKey}, maxPublicResponse)
		if err != nil {
			return sources, err
		}
		var page struct {
			Next  string `json:"nextPageToken"`
			Items []struct {
				ID      string `json:"id"`
				Snippet struct {
					Title      string `json:"title"`
					Channel    string `json:"videoOwnerChannelTitle"`
					ResourceID struct {
						Video string `json:"videoId"`
					} `json:"resourceId"`
				} `json:"snippet"`
				Details struct {
					Video string `json:"videoId"`
					Date  string `json:"videoPublishedAt"`
				} `json:"contentDetails"`
			} `json:"items"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return sources, fmt.Errorf("read YouTube playlist: %w", err)
		}
		for _, item := range page.Items {
			videoID := first(item.Details.Video, item.Snippet.ResourceID.Video)
			key := first(videoID, item.ID)
			if key == "" || videoIDs[key] {
				continue
			}
			videoIDs[key] = true
			var address string
			if providerID.MatchString(videoID) {
				address = "https://youtu.be/" + videoID
			}
			sources = append(sources, library.Source{
				ID: ingest.StableID("youtube", key), Title: first(item.Snippet.Title, key),
				URL: address, Author: item.Snippet.Channel, Date: item.Details.Date, Status: "unavailable",
			})
		}
		if page.Next == "" {
			return sources, nil
		}
		query.Set("pageToken", page.Next)
	}
}
