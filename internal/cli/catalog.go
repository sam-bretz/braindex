package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sam-bretz/braindex/internal/catalog"
	"github.com/sam-bretz/braindex/internal/library"
)

func (c command) catalog(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: braindex catalog list|install")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	switch args[0] {
	case "list":
		if len(args) != 2 {
			return errors.New("usage: braindex catalog list URL")
		}
		entries, err := catalog.List(ctx, args[1])
		if err != nil {
			return err
		}
		return c.json(entries)
	case "install":
		flags := newFlags("catalog install")
		version := flags.String("version", "", "pinned package version")
		brainID := flags.String("brain", "", "local brain ID")
		pos, err := parse(flags, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 2 || *version == "" {
			return errors.New("usage: braindex catalog install URL PACKAGE_ID --version VERSION [--brain ID]")
		}
		store, err := library.Open(c.root)
		if err != nil {
			return err
		}
		manifest, err := catalog.Install(ctx, store, pos[0], pos[1], *version, *brainID)
		if err != nil {
			return err
		}
		return c.json(manifest)
	default:
		return fmt.Errorf("unknown catalog command %q", args[0])
	}
}
