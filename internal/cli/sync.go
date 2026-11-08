package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sam-bretz/braindex/internal/connectors"
	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
)

func (c command) sync(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: braindex sync gmail|heptabase")
	}
	switch args[0] {
	case "gmail":
		return c.syncGmail(args[1:])
	case "heptabase":
		return c.syncHeptabase(args[1:])
	default:
		return fmt.Errorf("unknown sync connector %q", args[0])
	}
}

func (c command) syncGmail(args []string) error {
	flags := newFlags("sync gmail")
	label := flags.String("label", "", "Gmail label ID")
	brain := flags.String("brain", "", "destination brain ID")
	pos, err := parse(flags, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || *label == "" || *brain == "" {
		return errors.New("usage: braindex sync gmail --label LABEL_ID --brain ID")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	brains, err := store.Brains()
	if err != nil {
		return err
	}
	stored := map[string]bool{}
	found := false
	for _, candidate := range brains {
		if candidate.ID == *brain {
			found = true
			for _, id := range candidate.Sources {
				stored[id] = true
			}
			break
		}
	}
	if !found {
		return fmt.Errorf("unknown brain %q", *brain)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	token, err := gmailToken(ctx)
	if err != nil {
		return err
	}
	count, err := connectors.SyncGmail(ctx, *label, token, func(id string) bool {
		return stored[id]
	}, func(source library.Source) error {
		_, err := store.PutSource(source, []string{*brain})
		return err
	})
	if count > 0 || err == nil {
		err = errors.Join(err, index.Rebuild(store))
	}
	return errors.Join(c.json(struct {
		Saved int `json:"saved"`
	}{count}), err)
}
