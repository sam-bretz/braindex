package cli

import (
	"context"
	"errors"

	"github.com/sam-bretz/braindex/internal/hook"
	"github.com/sam-bretz/braindex/internal/library"
	"github.com/sam-bretz/braindex/internal/mcp"
)

func (c command) mcp(args []string) error {
	if len(args) != 1 || args[0] != "serve" {
		return errors.New("usage: braindex mcp serve")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	return mcp.Serve(context.Background(), store, c.in, c.out)
}

func (c command) hook(args []string) error {
	if len(args) != 1 || args[0] != "codex" {
		return errors.New("usage: braindex hook codex")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	return hook.Run(store, c.in, c.out)
}
