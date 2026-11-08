// Package mcp exposes Braindex retrieval over the local MCP protocol.
package mcp

import (
	"context"
	"io"

	"github.com/sam-bretz/braindex/internal/library"
	"github.com/sam-bretz/braindex/internal/retrieval"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type contextInput struct {
	Brains    []string `json:"brains,omitempty" jsonschema:"Specific brain IDs explicitly requested by the user"`
	AllBrains bool     `json:"all_brains,omitempty" jsonschema:"Search all allowed brains only when the user explicitly requests library-wide search"`
	Query     string   `json:"query" jsonschema:"Question to match against the user's topic rules and search for in selected brains"`
	Topic     string   `json:"topic,omitempty" jsonschema:"Optional explicit user-configured topic ID"`
	Limit     int      `json:"limit,omitempty" jsonschema:"Maximum excerpts per selected brain (1 to 10, defaults to 3)"`
}

func Serve(ctx context.Context, store *library.Store, in io.Reader, out io.Writer) error {
	reader, ok := in.(io.ReadCloser)
	if !ok {
		reader = io.NopCloser(in)
	}
	return newServer(store).Run(ctx, &sdk.IOTransport{
		Reader: reader,
		Writer: writeCloser{out},
	})
}

func newServer(store *library.Store) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "braindex", Version: "0.1.0"}, nil)
	closedWorld := false
	sdk.AddTool(server, &sdk.Tool{
		Name:        "braindex_context",
		Description: "Explain brain selection and return cited evidence grouped by brain. Use topic rules by default and broaden scope only when explicitly requested. Cite each contributing brain separately by title, locator, and revision. Report when a selected brain has no usable passages and label any synthesis separately.",
		Annotations: &sdk.ToolAnnotations{Title: "Search Braindex context", ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *sdk.CallToolRequest, input contextInput) (*sdk.CallToolResult, retrieval.Result, error) {
		result, err := retrieval.ContextWithContext(ctx, store, retrieval.Request{
			Query: input.Query, Topic: input.Topic, Limit: input.Limit, Brains: input.Brains, AllBrains: input.AllBrains,
		})
		return nil, result, err
	})
	return server
}

type writeCloser struct{ io.Writer }

func (writeCloser) Close() error { return nil }
