package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestClientConfigJSON verifies the block is valid JSON in the exact
// mcpServers shape MCP clients read, carrying the endpoint URL verbatim.
func TestClientConfigJSON(t *testing.T) {
	const endpoint = "http://127.0.0.1:49152/mcp"
	got := ClientConfigJSON(endpoint)

	var parsed struct {
		MCPServers map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("ClientConfigJSON produced invalid JSON: %v\n%s", err, got)
	}
	srv, ok := parsed.MCPServers[ClientConfigName]
	if !ok {
		t.Fatalf("config missing %q server entry:\n%s", ClientConfigName, got)
	}
	if srv.Type != "http" {
		t.Errorf("type = %q; want %q", srv.Type, "http")
	}
	if srv.URL != endpoint {
		t.Errorf("url = %q; want %q", srv.URL, endpoint)
	}
}

// TestClientConfigJSONIsIndented keeps the block paste-friendly: multi-line,
// two-space indentation.
func TestClientConfigJSONIsIndented(t *testing.T) {
	got := ClientConfigJSON("http://127.0.0.1:1/mcp")
	if !strings.Contains(got, "\n  \"mcpServers\"") {
		t.Errorf("config block is not indented as expected:\n%s", got)
	}
}

// TestClaudeMCPAddCommand pins the CLI one-liner shape.
func TestClaudeMCPAddCommand(t *testing.T) {
	const endpoint = "http://127.0.0.1:49152/mcp"
	got := ClaudeMCPAddCommand(endpoint)
	want := "claude mcp add --transport http msgbrowse " + endpoint
	if got != want {
		t.Errorf("ClaudeMCPAddCommand = %q; want %q", got, want)
	}
}
