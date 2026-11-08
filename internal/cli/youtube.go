package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sam-bretz/braindex/internal/connectors"
	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
)

const youtubeCaptionsUsage = "usage: braindex source youtube-captions SOURCE_ID... [--lang en] (up to 10 sources)"

type youtubeCaptionResult struct {
	SourceID string `json:"source_id"`
	Status   string `json:"status"`
	Language string `json:"language"`
	Cached   bool   `json:"cached"`
	Revision string `json:"revision,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Next     string `json:"next,omitempty"`
}

func youtubeExtractor() connectors.YouTubeExtractor {
	return connectors.YouTubeExtractor{
		Executable: os.Getenv("BRAINDEX_YT_DLP"),
		JSRuntime:  os.Getenv("BRAINDEX_YT_DLP_JS_RUNTIME"),
	}
}

func (c command) youtubeCaptions(store *library.Store, args []string) error {
	return c.youtubeCaptionsSelected(store, args, nil)
}

func (c command) youtubeCaptionsSelected(store *library.Store, args, selected []string) error {
	flags := newFlags("source youtube-captions")
	language := flags.String("lang", "en", "caption language code")
	ids, err := parse(flags, args)
	if err != nil {
		return err
	}
	if selected != nil {
		if len(ids) != 0 {
			return errors.New("adapter arguments cannot contain additional source IDs")
		}
		ids = selected
	}
	if len(ids) == 0 || len(ids) > 10 {
		return errors.New(youtubeCaptionsUsage)
	}
	if err := connectors.ValidateYouTubeCaptionLanguage(*language); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	extractor := youtubeExtractor()
	results := make([]youtubeCaptionResult, 0, len(ids))
	seen := make(map[string]bool)
	var rateLimited, failed, ready bool
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		result := youtubeCaptionResult{SourceID: id, Language: *language}
		saved, cached, err := attachYouTubeCaption(ctx, store, extractor, id, *language, rateLimited)
		switch {
		case err == nil:
			ready = true
			result.Status, result.Cached, result.Revision = "searchable", cached, saved.Revision
		case errors.Is(err, connectors.ErrYouTubeCaptionUnavailable):
			result.Status, result.Detail, result.Next = "unavailable", err.Error(), "try another caption language or attach a local transcript"
		case errors.Is(err, connectors.ErrYouTubeRateLimited):
			rateLimited, result.Status, result.Detail, result.Next = true, "rate_limited", err.Error(), "retry later"
		default:
			failed, result.Status, result.Detail, result.Next = true, "failed", err.Error(), "check the detail and retry this source"
		}
		results = append(results, result)
	}
	var statusErr error
	if rateLimited {
		statusErr = errors.New("YouTube rate limited caption fetching; try again later")
	}
	if failed {
		statusErr = errors.Join(statusErr, errors.New("one or more caption operations failed; see the result details"))
	}
	if selected != nil && ready {
		statusErr = errors.Join(statusErr, index.Rebuild(store))
	}
	return errors.Join(c.json(results), statusErr)
}

func attachYouTubeCaption(ctx context.Context, store *library.Store, extractor connectors.YouTubeExtractor, sourceID, language string, rateLimited bool) (library.Source, bool, error) {
	source, err := store.Source(sourceID)
	if err != nil {
		return library.Source{}, false, err
	}
	videoID, err := connectors.YouTubeVideoID(source.URL)
	if err != nil {
		return library.Source{}, false, err
	}
	cache := filepath.Join(store.MetaDir(), "media", "youtube", ingest.StableID("youtube", videoID))
	path := filepath.Join(cache, language+".vtt")
	attached, err := ingest.AttachTranscript(source, path)
	if err == nil {
		if attached.Status != "ready" {
			return library.Source{}, false, errors.New("cached caption has no readable text")
		}
		saved, err := store.PutTranscript(sourceID, path, attached.Passages)
		return saved, true, err
	}
	if rateLimited {
		return library.Source{}, false, fmt.Errorf("%w; no further downloads were attempted", connectors.ErrYouTubeRateLimited)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return library.Source{}, false, fmt.Errorf("read cached caption: %w", err)
	}
	if err := os.MkdirAll(cache, 0700); err != nil {
		return library.Source{}, false, err
	}
	stage, err := os.MkdirTemp(cache, ".download-")
	if err != nil {
		return library.Source{}, false, err
	}
	defer os.RemoveAll(stage)
	fetchCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	caption, err := extractor.FetchCaption(fetchCtx, videoID, language, stage)
	if err != nil {
		return library.Source{}, false, err
	}
	attached, err = ingest.AttachTranscript(source, caption)
	if err != nil {
		return library.Source{}, false, err
	}
	if attached.Status != "ready" {
		return library.Source{}, false, connectors.ErrYouTubeCaptionUnavailable
	}
	if err := os.Chmod(caption, 0600); err != nil {
		return library.Source{}, false, err
	}
	// Publish a complete file without replacing the track published by a
	// concurrent command. If another downloader won, use the same cached data.
	cached := false
	if err := os.Link(caption, path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return library.Source{}, false, err
		}
		attached, err = ingest.AttachTranscript(source, path)
		if err != nil {
			return library.Source{}, false, err
		}
		cached = true
	}
	saved, err := store.PutTranscript(sourceID, path, attached.Passages)
	return saved, cached, err
}
