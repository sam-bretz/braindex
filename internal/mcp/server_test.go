package mcp

import (
	"context"
	"encoding/json"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
	"github.com/sam-bretz/braindex/internal/retrieval"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServeReadOnlyContextOverStdioTransport(t *testing.T) {
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sales", "markets", "private"} {
		if err := store.CreateBrain(library.Brain{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.PutSource(library.Source{Title: id, Passages: []library.Passage{
			{Text: id + " discovery questions", Locator: "page 9"},
		}}, []string{id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := retrieval.SaveRule(store, retrieval.Rule{ID: "sales", Brains: []string{"sales", "markets", "private"}, Phrases: []string{"discovery"}}); err != nil {
		t.Fatal(err)
	}
	if err := retrieval.SetBrainAccess(store, "private", false); err != nil {
		t.Fatal(err)
	}
	if err := index.Rebuild(store); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, store, serverConn, serverConn) }()
	client := sdk.NewClient(&sdk.Implementation{Name: "fixture", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &sdk.IOTransport{Reader: clientConn, Writer: clientConn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "braindex_context" ||
		tools.Tools[0].Annotations == nil || !tools.Tools[0].Annotations.ReadOnlyHint {
		t.Fatalf("unexpected tools: %#v", tools.Tools)
	}

	for _, query := range []string{"sales discovery", "unrelated"} {
		expected, err := retrieval.Context(store, retrieval.Request{Query: query})
		if err != nil {
			t.Fatal(err)
		}
		got, err := session.CallTool(ctx, &sdk.CallToolParams{
			Name: "braindex_context", Arguments: map[string]any{"query": query},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.IsError {
			t.Fatalf("MCP returned a tool error: %#v", got.Content)
		}
		data, err := json.Marshal(got.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var result retrieval.Result
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if query == "sales discovery" {
			if result.Routing.Scope != "matched_rules" || len(result.Brains) != 3 {
				t.Fatalf("missing routed evidence: %+v", result)
			}
			for i, name := range []string{"sales", "markets"} {
				brain := result.Brains[i]
				if brain.ID != name || !brain.Searched || len(brain.Excerpts) != 1 ||
					!reflect.DeepEqual(brain.SelectedByTopics, []string{"sales"}) ||
					brain.Excerpts[0].Title != name || brain.Excerpts[0].Locator != "page 9" || brain.Excerpts[0].Revision == "" {
					t.Fatalf("missing cited evidence for %s: %+v", name, brain)
				}
			}
			if denied := result.Brains[2]; denied.Status != "denied" || denied.Searched || len(denied.Excerpts) != 0 {
				t.Fatalf("access policy was not applied: %+v", denied)
			}
		}
		if !reflect.DeepEqual(expected, result) {
			t.Fatalf("MCP result differs from CLI retrieval: got %#v; want %#v", result, expected)
		}
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
