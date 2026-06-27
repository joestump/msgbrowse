package cli

import (
	"fmt"

	"github.com/joestump/msgbrowse/internal/ingest"
	"github.com/spf13/cobra"
)

// newSignalImportCommand wires the Signal-side importer.
//
// msgbrowse holds data from multiple message-archive sources at once and gives
// each one its own top-level import subcommand. The Signal importer reads a
// signal-export Markdown archive (per-conversation chat.md + media/ + the
// .snapshots/*.tar inventory) and writes to the unified store, tagging every
// row with source="signal". See internal/source for the canonical names.
//
// The iMessage importer (`imessage-import`) is a sibling that lands in Slice
// 2.5; both share the same store and ingest_state pattern.
func newSignalImportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "signal-import",
		Short: "Import (or refresh) a signal-export archive into the local store",
		Long: "signal-import scans a read-only signal-export archive, parses each changed\n" +
			"conversation's chat.md into the unified SQLite store, and refreshes the\n" +
			"snapshot inventory. It is incremental and idempotent: unchanged\n" +
			"conversations are skipped, so re-running it is cheap.\n" +
			"\n" +
			"Every imported row is tagged source=\"signal\". For Apple iMessage exports\n" +
			"use imessage-import (Slice 2.5).",
		RunE: runSignalImport,
	}
	cmd.Flags().Bool("full", false, "ignore incremental state and re-scan every conversation")
	return cmd
}

// newIngestAliasCommand keeps `msgbrowse ingest` working as a hidden alias for
// `signal-import` for one release, so callers who scripted against the
// pre-rename command keep working while they migrate.
func newIngestAliasCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "ingest",
		Short:  "Deprecated alias for signal-import",
		Hidden: true,
		RunE:   runSignalImport,
	}
	cmd.Flags().Bool("full", false, "ignore incremental state and re-scan every conversation")
	return cmd
}

// runSignalImport is the shared body for both signal-import and the deprecated
// `ingest` alias.
func runSignalImport(cmd *cobra.Command, _ []string) error {
	cfg, err := resolveConfig()
	if err != nil {
		return err
	}
	if err := requireArchive(cfg); err != nil {
		return err
	}
	full, err := cmd.Flags().GetBool("full")
	if err != nil {
		return err
	}

	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	run, err := ingest.Run(cmd.Context(), st, ingest.Options{
		ArchiveRoot: cfg.ArchiveRoot,
		Full:        full,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(),
		"signal-import: %d/%d conversations changed, %d messages total (%d added), %d snapshots, %d skipped lines in %dms\n",
		run.ConversationsChanged, run.ConversationsScanned, run.MessagesTotal, run.MessagesAdded,
		run.SnapshotsSeen, run.SkippedLines, run.DurationMS)
	return err
}
