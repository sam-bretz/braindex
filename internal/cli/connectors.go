package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/sam-bretz/braindex/internal/connectors"
	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
)

func (c command) discover(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: braindex discover web|rss|podcast|youtube URL --brain ID [--create-brain]")
	}
	flags := newFlags("discover " + args[0])
	create := flags.Bool("create-brain", false, "create the destination brain when missing")
	var brains stringsFlag
	flags.Var(&brains, "brain", "destination brain ID")
	pos, err := parse(flags, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 || len(brains) == 0 {
		return errors.New("usage: braindex discover web|rss|podcast|youtube URL --brain ID [--create-brain]")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	if !*create {
		if err := requireBrains(store, brains); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var sources []library.Source
	switch args[0] {
	case "web":
		source, fetchErr := connectors.FetchWeb(ctx, pos[0])
		if fetchErr != nil {
			return fetchErr
		}
		sources = []library.Source{source}
	case "rss":
		sources, err = connectors.FetchFeed(ctx, pos[0])
	case "podcast":
		sources, err = connectors.FetchPodcast(ctx, pos[0])
	case "youtube":
		sources, err = connectors.FetchYouTubePlaylist(ctx, pos[0], os.Getenv("BRAINDEX_YOUTUBE_API_KEY"))
	default:
		return fmt.Errorf("unknown discovery connector %q", args[0])
	}
	if len(sources) == 0 && err != nil {
		return err
	}
	if *create {
		for _, brain := range brains {
			if createErr := store.CreateBrain(library.Brain{ID: brain}); createErr != nil && !errors.Is(createErr, library.ErrExists) {
				return createErr
			}
		}
	}
	var saved []library.Source
	for _, source := range sources {
		if source.ID != "" {
			old, lookupErr := store.Source(source.ID)
			if lookupErr != nil && !errors.Is(lookupErr, library.ErrNotFound) {
				return lookupErr
			}
			if lookupErr == nil {
				source.Redistribution = old.Redistribution
				if (args[0] == "podcast" || args[0] == "youtube") && len(source.Passages) == 0 && len(old.Passages) > 0 {
					source.Path, source.Status, source.Passages = old.Path, old.Status, old.Passages
				}
			}
		}
		record, saveErr := store.PutSource(source, brains)
		if saveErr != nil {
			return saveErr
		}
		record.Passages = nil
		saved = append(saved, record)
	}
	if len(saved) == 0 {
		saved = []library.Source{}
	}
	return errors.Join(c.json(saved), err)
}

func requireBrains(store *library.Store, ids []string) error {
	brains, err := store.Brains()
	if err != nil {
		return err
	}
	exists := make(map[string]bool, len(brains))
	for _, brain := range brains {
		exists[brain.ID] = true
	}
	for _, id := range ids {
		if !exists[id] {
			return fmt.Errorf("unknown brain %q", id)
		}
	}
	return nil
}

func (c command) transcript(store *library.Store, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: braindex source transcript SOURCE_ID PATH")
	}
	source, err := store.Source(args[0])
	if err != nil {
		return err
	}
	source, err = ingest.AttachTranscript(source, args[1])
	if err != nil {
		return err
	}
	return c.saveSource(store, source, nil)
}
