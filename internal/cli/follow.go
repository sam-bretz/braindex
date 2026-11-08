package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sam-bretz/braindex/internal/connectors"
	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
)

const followUsage = "usage: braindex follow add|list|refresh|process|save"

type followItemResult struct {
	library.FollowItem
	Change string `json:"change"`
	Next   string `json:"next,omitempty"`
}

type followRefreshResult struct {
	ID     string             `json:"id"`
	Kind   string             `json:"kind"`
	Status string             `json:"status"`
	Next   string             `json:"next,omitempty"`
	Error  string             `json:"error,omitempty"`
	Items  []followItemResult `json:"items"`
}

func (c command) follow(args []string) error {
	if len(args) == 0 {
		return errors.New(followUsage)
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		return c.followAdd(store, args[1:])
	case "list":
		if len(args) != 1 {
			return errors.New("usage: braindex follow list")
		}
		follows, err := store.Follows()
		if err != nil {
			return err
		}
		return c.json(follows)
	case "refresh":
		return c.followRefresh(store, args[1:])
	case "process":
		return c.followProcess(store, args[1:])
	case "save":
		return c.followSave(store, args[1:])
	default:
		return errors.New(followUsage)
	}
}

func (c command) followAdd(store *library.Store, args []string) error {
	flags := newFlags("follow add")
	brain := flags.String("brain", "", "destination brain")
	kind := flags.String("kind", "rss", "rss, podcast, or youtube")
	author := flags.String("author", "", "follow a reviewed author scope")
	hint := flags.String("hint", "", "public hint for an author search")
	ytdlp := flags.Bool("yt-dlp", false, "use the optional yt-dlp adapter for YouTube")
	allowPrivate := flags.Bool("allow-private", false, "permit a trusted private RSS or podcast endpoint")
	pos, err := parse(flags, args)
	if err != nil {
		return err
	}
	if *brain == "" || len(pos) < 1 || len(pos) > 2 || (*author == "" && len(pos) != 2) ||
		(*author != "" && (len(pos) != 1 || *kind != "rss")) || (*ytdlp && *kind != "youtube") || (*hint != "" && *author == "") || (*allowPrivate && (*author != "" || *kind == "youtube")) {
		return errors.New("usage: braindex follow add ID URL --brain ID [--kind rss|podcast|youtube] [--yt-dlp | --allow-private]; or follow add ID --brain ID --author NAME [--hint TOPIC]")
	}
	if err := requireBrains(store, []string{*brain}); err != nil {
		return err
	}
	follow := library.Follow{ID: pos[0], Kind: *kind, Brain: *brain, AllowPrivate: *allowPrivate}
	if *author != "" {
		follow.Kind, follow.Author, follow.Hint = "author", strings.TrimSpace(*author), strings.TrimSpace(*hint)
		if follow.Author == "" || len(follow.Author)+len(follow.Hint) > 400 || len(strings.Fields(follow.Author+" "+follow.Hint)) > 60 {
			return errors.New("provide an author name and optional short public hint")
		}
	} else {
		switch *kind {
		case "rss", "podcast", "youtube":
		default:
			return fmt.Errorf("unknown follow kind %q", *kind)
		}
		parsed, err := url.Parse(pos[1])
		if err != nil || parsed.User != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("follow requires an HTTP(S) URL without credentials")
		}
		parsed.Fragment = ""
		follow.URL = parsed.String()
		if *ytdlp {
			follow.Adapter = "yt-dlp"
		}
	}
	if err := store.AddFollow(follow); err != nil {
		return err
	}
	return c.json(follow)
}

func (c command) followRefresh(store *library.Store, args []string) error {
	flags := newFlags("follow refresh")
	file := flags.String("results", "", "review an author results file")
	search := flags.Bool("search", false, "send stored public author scope to Brave Search")
	pos, err := parse(flags, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 || (*file != "" && (len(pos) != 1 || *search)) {
		return errors.New("usage: braindex follow refresh [ID] [--results FILE | --search]")
	}
	follows, err := store.Follows()
	if err != nil {
		return err
	}
	if len(pos) == 1 {
		follow, err := store.Follow(pos[0])
		if err != nil {
			return err
		}
		if (*file != "" || *search) && follow.Kind != "author" {
			return errors.New("author search options require an author follow")
		}
		follows = []library.Follow{follow}
	}
	reports := make([]followRefreshResult, 0, len(follows))
	reindex, failed := false, 0
	for _, follow := range follows {
		report := followRefreshResult{ID: follow.ID, Kind: follow.Kind, Status: "ok", Items: []followItemResult{}}
		if follow.Kind == "author" && *file == "" && !*search {
			report.Status, report.Next = "needs_results", "refresh with --results FILE or --search"
			reports = append(reports, report)
			continue
		}
		err := store.UpdateFollow(follow.ID, func(current *library.Follow) error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if current.Kind == "author" {
				return refreshAuthor(ctx, current, &report, *file, *search)
			}
			return refreshMedia(ctx, store, current, &report, &reindex)
		})
		if err != nil {
			report.Status, report.Error = "failed", err.Error()
			failed++
		}
		reports = append(reports, report)
	}
	var resultErr error
	if failed > 0 {
		resultErr = fmt.Errorf("%d follow refresh(es) failed; see results", failed)
	}
	if reindex {
		snapshot, err := store.Snapshot()
		if err == nil && index.InspectKeyword(store, snapshot.Sources).State != "current" {
			err = index.Rebuild(store)
		}
		resultErr = errors.Join(resultErr, err)
	}
	return errors.Join(c.json(reports), resultErr)
}

func refreshMedia(ctx context.Context, store *library.Store, current *library.Follow, report *followRefreshResult, reindex *bool) error {
	var sources []library.Source
	var err error
	switch current.Kind {
	case "rss", "podcast":
		sources, err = connectors.FetchFollowFeed(ctx, current.URL, current.Kind == "podcast", current.AllowPrivate)
	case "youtube":
		if current.Adapter == "yt-dlp" {
			sources, err = youtubeExtractor().Discover(ctx, current.URL, 50)
		} else {
			sources, err = connectors.FetchYouTubePlaylist(ctx, current.URL, os.Getenv("BRAINDEX_YOUTUBE_API_KEY"))
		}
	default:
		return errors.New("unknown saved follow kind")
	}
	if err != nil {
		return err
	}
	known := followItems(current.Items)
	seen := make(map[string]bool)
	for _, source := range sources {
		if seen[source.ID] {
			continue
		}
		seen[source.ID] = true
		saved, err := store.PutDiscoveredSource(source, []string{current.Brain}, current.Kind != "rss")
		if err != nil {
			return err
		}
		item := library.FollowItem{ID: saved.ID, Title: saved.Title, URL: saved.URL, Revision: saved.Revision, Status: saved.Status}
		next := ""
		if saved.Status != "ready" {
			item.Status = "waiting"
			switch {
			case current.Kind == "youtube" || saved.AudioURL != "":
				next = "process"
			default:
				next = "attach_transcript_or_text"
			}
		} else {
			// A transcript may have been attached since the prior refresh.
			*reindex = true
		}
		appendFollowResult(report, known, item, next)
	}
	current.Items = sortedFollowItems(known)
	return nil
}

func refreshAuthor(ctx context.Context, current *library.Follow, report *followRefreshResult, file string, search bool) error {
	var candidates []connectors.PersonCandidate
	var err error
	switch {
	case file != "":
		candidates, err = readPersonResults(file)
	case search:
		candidates, err = connectors.SearchPerson(ctx, current.Author, current.Hint, os.Getenv("BRAVE_SEARCH_API_KEY"))
	default:
		return errors.New("author follow requires --results FILE or explicit --search")
	}
	if err != nil {
		return err
	}
	known := followItems(current.Items)
	for _, candidate := range connectors.PersonCandidates(candidates) {
		id := ingest.StableID("candidate", candidate.URL)
		item := library.FollowItem{ID: id, Title: candidate.Title, URL: candidate.URL, Kind: candidate.Kind, Status: "review"}
		next := "save"
		if known[id].Status == "saved" {
			item.Status, item.Revision, next = "saved", known[id].Revision, ""
		}
		appendFollowResult(report, known, item, next)
	}
	current.Items = sortedFollowItems(known)
	return nil
}

func appendFollowResult(report *followRefreshResult, known map[string]library.FollowItem, item library.FollowItem, next string) {
	previous, found := known[item.ID]
	change := "unchanged"
	switch {
	case !found:
		change = "new"
	case previous.Status != "ready" && item.Status == "ready":
		change = "became_searchable"
	case previous != item:
		change = "changed"
	}
	known[item.ID] = item
	report.Items = append(report.Items, followItemResult{item, change, next})
}

func followItems(items []library.FollowItem) map[string]library.FollowItem {
	byID := make(map[string]library.FollowItem, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	return byID
}

func sortedFollowItems(items map[string]library.FollowItem) []library.FollowItem {
	result := make([]library.FollowItem, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (c command) followProcess(store *library.Store, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: braindex follow process ID SOURCE_ID... [-- adapter flags]")
	}
	follow, err := store.Follow(args[0])
	if err != nil {
		return err
	}
	ids, options := args[1:], []string(nil)
	for i, arg := range ids {
		if arg == "--" {
			ids, options = ids[:i], ids[i+1:]
			break
		}
	}
	if len(ids) == 0 {
		return errors.New("select at least one source ID")
	}
	known := followItems(follow.Items)
	for _, id := range ids {
		if _, exists := known[id]; !exists {
			return fmt.Errorf("source %q is not part of follow %q; refresh first", id, follow.ID)
		}
	}
	switch follow.Kind {
	case "podcast":
		return c.transcribePodcastSelected(store, options, ids, connectors.DownloadPodcastAudio)
	case "youtube":
		return c.youtubeCaptionsSelected(store, options, ids)
	default:
		return errors.New("process is available for podcast and YouTube follows")
	}
}

func (c command) followSave(store *library.Store, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: braindex follow save ID CANDIDATE_ID...")
	}
	var saved []library.FollowItem
	err := store.UpdateFollow(args[0], func(follow *library.Follow) error {
		if follow.Kind != "author" {
			return errors.New("save is available for reviewed author candidates")
		}
		items := followItems(follow.Items)
		for _, id := range args[1:] {
			if _, exists := items[id]; !exists {
				return fmt.Errorf("unknown candidate %q", id)
			}
		}
		for _, id := range args[1:] {
			item := items[id]
			if item.Status == "saved" {
				continue
			}
			source, err := c.savePersonCandidate(context.Background(), store, follow.Brain,
				connectors.PersonCandidate{Title: item.Title, URL: item.URL, Kind: item.Kind}, false)
			if err != nil {
				return err
			}
			item.Status, item.Revision = "saved", source.Revision
			items[id] = item
			saved = append(saved, item)
		}
		follow.Items = sortedFollowItems(items)
		return nil
	})
	if saved == nil {
		saved = []library.FollowItem{}
	}
	return errors.Join(c.json(saved), err)
}
