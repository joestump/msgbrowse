package cli

import (
	"fmt"

	"github.com/joestump/sigbrowse/internal/ingest"
	"github.com/spf13/cobra"
)

func newIngestCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Scan the archive and (incrementally) populate the database",
		Long: "ingest scans the read-only signal-export archive, parses each changed\n" +
			"conversation's chat.md into the SQLite database, and refreshes the snapshot\n" +
			"inventory. It is incremental and idempotent: unchanged conversations are\n" +
			"skipped, so re-running it is cheap.",
		RunE: func(cmd *cobra.Command, _ []string) error {
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
				"ingested %d/%d conversations changed, %d messages total (%d added), %d snapshots, %d skipped lines in %dms\n",
				run.ConversationsChanged, run.ConversationsScanned, run.MessagesTotal, run.MessagesAdded,
				run.SnapshotsSeen, run.SkippedLines, run.DurationMS)
			return err
		},
	}
	cmd.Flags().Bool("full", false, "ignore incremental state and re-scan every conversation")
	return cmd
}
