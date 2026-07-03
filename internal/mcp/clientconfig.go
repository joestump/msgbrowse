// Client-configuration blocks for connecting an MCP client (Claude Desktop,
// Claude Code) to a msgbrowse MCP endpoint served over streamable HTTP.
//
// This is the single source for the copy-paste config the app hands out: the
// desktop shell's menubar "Copy MCP Config" action uses it today, and the
// SPEC-0010 /settings Connect page consumes the same builders when it lands
// (issue #100), so the tray and the web page can never drift apart. It lives
// in the core module — pure Go, no cgo — so it stays testable with
// CGO_ENABLED=0 (ADR-0013).
//
// Governing: SPEC-0010 REQ "Menubar quick menu" (the full JSON
// client-configuration block) and REQ "Connect/Settings page in the web app"
// (the JSON block and the equivalent `claude mcp add` command line).
package mcp

import (
	"encoding/json"
	"fmt"
)

// ClientConfigName is the server name msgbrowse registers under in an MCP
// client's configuration.
const ClientConfigName = "msgbrowse"

// ClientConfigJSON returns the copy-paste JSON configuration block that
// connects an MCP client to the streamable-HTTP endpoint at endpointURL,
// in the `mcpServers` shape Claude Desktop and Claude Code read.
func ClientConfigJSON(endpointURL string) string {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			ClientConfigName: map[string]string{
				"type": "http",
				"url":  endpointURL,
			},
		},
	}
	// Marshalling string-keyed maps of strings cannot fail; MarshalIndent is
	// used so the block is valid JSON *and* pleasant to paste.
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "" // unreachable; keeps the API a plain string
	}
	return string(b)
}

// ClaudeMCPAddCommand returns the `claude mcp add` one-liner equivalent to
// the ClientConfigJSON block for the same streamable-HTTP endpoint.
func ClaudeMCPAddCommand(endpointURL string) string {
	return fmt.Sprintf("claude mcp add --transport http %s %s", ClientConfigName, endpointURL)
}
