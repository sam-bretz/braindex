package cli

import (
	"context"
	"errors"
	"time"

	"github.com/sam-bretz/braindex/internal/connectors"
	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
)

func (c command) syncHeptabase(args []string) error {
	flags := newFlags("sync heptabase")
	tag := flags.String("tag", "", "Heptabase tag ID")
	board := flags.String("whiteboard", "", "Heptabase whiteboard ID")
	brain := flags.String("brain", "", "destination brain ID")
	pos, err := parse(flags, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || *brain == "" || (*tag == "") == (*board == "") {
		return errors.New("usage: braindex sync heptabase (--tag ID | --whiteboard ID) --brain ID")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	if err := requireBrains(store, []string{*brain}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	report := struct {
		Imported    int `json:"imported"`
		Unavailable int `json:"unavailable"`
	}{}
	err = connectors.SyncHeptabase(ctx, connectors.HeptabaseScope{Tag: *tag, Whiteboard: *board}, func(source library.Source) error {
		saved, err := store.PutSource(source, []string{*brain})
		if err == nil {
			report.Imported++
			if saved.Status == "unavailable" {
				report.Unavailable++
			}
		}
		return err
	})
	if report.Imported > 0 || err == nil {
		err = errors.Join(err, index.Rebuild(store))
	}
	return errors.Join(c.json(report), err)
}
