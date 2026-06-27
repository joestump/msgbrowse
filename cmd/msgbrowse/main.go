// Command msgbrowse is a self-hosted, local-only browser, search engine, and
// AI-editorialized journal over your message archives.
//
// It imports from one or more upstream exporters (today: signal-export; Slice
// 2.5 adds imessage-exporter) into a unified SQLite store and exposes several
// subcommands:
//
//	msgbrowse signal-import    import a signal-export Markdown archive
//	msgbrowse imessage-import  (Slice 2.5) import an imessage-exporter archive
//	msgbrowse serve            run the local HTMX web UI
//	msgbrowse mcp              run the Model Context Protocol server
//	msgbrowse watch            re-ingest automatically when an archive changes
//	msgbrowse journal          (re)build the day-by-day journal + LLM digests
//
// Every imported archive is treated as strictly read-only. See README.md and
// ARCHITECTURE.md for the full design; SECURITY.md for the egress model.
package main

import (
	"fmt"
	"os"

	"github.com/joestump/msgbrowse/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "msgbrowse:", err)
		os.Exit(1)
	}
}
