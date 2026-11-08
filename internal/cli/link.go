package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/sam-bretz/braindex/internal/connectors"
	"github.com/sam-bretz/braindex/internal/inbox"
	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
)

const linkUsage = "usage: braindex link URL [--brain ID | --new-brain ID | --inbox] [--limit N] [--json | --select SOURCE_ID]"

type linkItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	URL    string `json:"url,omitempty"`
	Status string `json:"status"`
}

type linkReview struct {
	Kind   string     `json:"kind"`
	URL    string     `json:"url"`
	Detail string     `json:"detail,omitempty"`
	Items  []linkItem `json:"items"`
}

func (c command) link(args []string) error {
	return c.linkUsing(args, func(ctx context.Context, address string, limit int) (connectors.LinkPreview, error) {
		return connectors.PreviewLink(ctx, address, limit, youtubeExtractor(), os.Getenv("BRAINDEX_YOUTUBE_API_KEY"))
	})
}

func (c command) linkUsing(args []string, probe func(context.Context, string, int) (connectors.LinkPreview, error)) error {
	flags := newFlags("link")
	brain := flags.String("brain", "", "save to an existing brain")
	newBrain := flags.String("new-brain", "", "create a brain for the selected sources")
	inboxOnly := flags.Bool("inbox", false, "stage selected URLs in the inbox")
	limit := flags.Int("limit", 20, "maximum items to preview")
	asJSON := flags.Bool("json", false, "preview without saving")
	var selectedIDs stringsFlag
	flags.Var(&selectedIDs, "select", "save a source ID from a prior preview; repeat to select several")
	pos, err := parse(flags, args)
	if err != nil {
		return err
	}
	destinations := 0
	for _, supplied := range []bool{*brain != "", *newBrain != "", *inboxOnly} {
		if supplied {
			destinations++
		}
	}
	if len(pos) != 1 || *limit < 1 || *limit > 50 || destinations > 1 ||
		(*asJSON && (len(selectedIDs) > 0 || destinations > 0)) || (len(selectedIDs) > 0 && destinations != 1) {
		return errors.New(linkUsage)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	preview, err := probe(ctx, pos[0], *limit)
	if err != nil {
		return err
	}
	review := linkReview{Kind: preview.Kind, URL: preview.URL, Detail: preview.Detail, Items: []linkItem{}}
	for _, source := range preview.Sources {
		id := source.ID
		if id == "" {
			id = ingest.StableID("link", source.URL)
		}
		review.Items = append(review.Items, linkItem{id, source.Title, source.URL, source.Status})
	}
	if *asJSON {
		return c.json(review)
	}
	fmt.Fprintf(c.out, "%s · %s\n", review.Kind, terminalLine(review.URL))
	if review.Detail != "" {
		fmt.Fprintln(c.out, terminalLine(review.Detail))
	}
	for i, item := range review.Items {
		fmt.Fprintf(c.out, "%2d. [%s] %s\n", i+1, item.Status, terminalLine(item.Title))
		if item.URL != "" {
			fmt.Fprintf(c.out, "    %s\n", terminalLine(item.URL))
		}
	}
	if len(review.Items) == 0 {
		return nil
	}
	var selected []int
	input := bufio.NewScanner(c.inOrEmpty())
	input.Buffer(make([]byte, 256), 4096)
	if len(selectedIDs) == 0 {
		fmt.Fprint(c.out, "Select item numbers (for example 1,3), or press Enter to cancel: ")
		if input.Scan() {
			selected, err = personSelections(input.Text(), len(review.Items))
		}
		if err := errors.Join(err, input.Err()); err != nil {
			return err
		}
	} else {
		indexes := make(map[string]int, len(review.Items))
		for i, item := range review.Items {
			indexes[item.ID] = i + 1
		}
		for _, id := range selectedIDs {
			i, ok := indexes[id]
			if !ok {
				return fmt.Errorf("source %q is no longer in the preview", id)
			}
			selected = append(selected, i)
			delete(indexes, id)
		}
	}
	if len(selected) == 0 {
		return nil
	}
	if destinations == 0 {
		fmt.Fprint(c.out, "Destination (brain:ID, new:ID, inbox), or press Enter to cancel: ")
		if !input.Scan() {
			return input.Err()
		}
		value := strings.TrimSpace(input.Text())
		switch {
		case value == "":
			return nil
		case value == "inbox":
			*inboxOnly = true
		case strings.HasPrefix(value, "brain:"):
			*brain = strings.TrimPrefix(value, "brain:")
		case strings.HasPrefix(value, "new:"):
			*newBrain = strings.TrimPrefix(value, "new:")
		}
		if *brain == "" && *newBrain == "" && !*inboxOnly {
			return errors.New("choose brain:ID, new:ID, or inbox")
		}
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	if *newBrain != "" {
		if err := store.CreateBrain(library.Brain{ID: *newBrain, Name: *newBrain}); err != nil {
			return err
		}
		*brain = *newBrain
	} else if *brain != "" {
		if err := requireBrains(store, []string{*brain}); err != nil {
			return err
		}
	}
	if *inboxOnly {
		for _, number := range selected {
			if preview.Sources[number-1].URL == "" {
				return fmt.Errorf("item %d has no URL to stage", number)
			}
		}
	}
	ready := false
	for _, number := range selected {
		source := preview.Sources[number-1]
		if *inboxOnly {
			item, err := inbox.Capture(store, "url", source.URL)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.out, "Staged %s in the inbox.\n", item.ID)
			continue
		}
		source, extractErr := preview.SelectedSource(number - 1)
		if extractErr != nil {
			fmt.Fprintln(c.out, extractErr)
		}
		var saved library.Source
		if preview.Kind == "reference" || extractErr != nil {
			saved, err = store.PutReference(source, []string{*brain})
		} else {
			saved, err = store.PutDiscoveredSource(source, []string{*brain}, preview.Kind == "youtube" || preview.Kind == "podcast")
		}
		if err != nil {
			return err
		}
		ready = ready || saved.Status == "ready"
		fmt.Fprintf(c.out, "Saved %s to %s (%s).\n", saved.ID, *brain, saved.Status)
	}
	if ready {
		return index.Rebuild(store)
	}
	return nil
}

func (c command) inOrEmpty() io.Reader {
	if c.in == nil {
		return strings.NewReader("")
	}
	return c.in
}

func terminalLine(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, value)
}
