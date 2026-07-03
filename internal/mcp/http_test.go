package mcp

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestHTTPHandlerServesStreamableMCP proves the handler returned by
// HTTPHandler speaks the full streamable-HTTP protocol when mounted on a
// caller-owned listener — the shape the desktop shell uses when it mounts MCP
// at /mcp on the embedded web server (SPEC-0010 "Menubar quick menu" status
// line / bind surface).
func TestHTTPHandlerServesStreamableMCP(t *testing.T) {
	st, _ := seedStore(t)
	srv := NewServer(st, nil, Options{
		Version: "test",
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	ts := httptest.NewServer(srv.HTTPHandler())
	defer ts.Close()

	ctx := context.Background()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatalf("connect over streamable HTTP: %v", err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools listed over the HTTP transport")
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "search_messages" {
			found = true
		}
	}
	if !found {
		t.Errorf("search_messages not registered over HTTP; got %d tools", len(tools.Tools))
	}
}
