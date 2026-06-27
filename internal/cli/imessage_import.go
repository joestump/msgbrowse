package cli

import (
	"fmt"

	"github.com/joestump/msgbrowse/internal/imessage"
	"github.com/spf13/cobra"
)

func newIMessageImportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "imessage-import",
		Short: "Import (or refresh) an imessage-exporter archive into the local store",
		Long: "imessage-import scans a read-only imessage-exporter archive (a flat\n" +
			"directory of <ChatName>.txt files produced by `imessage-exporter -f txt`),\n" +
			"parses each changed conversation into the unified SQLite store, and tags every\n" +
			"row source=\"imessage\". Incremental and idempotent, like signal-import.\n" +
			"\n" +
			"Targets the imessage-exporter 4.2.0 txt format; the path comes from\n" +
			"imessage_archive_root / MSGBROWSE_IMESSAGE_ARCHIVE_ROOT / --imessage-archive-root.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			if err := requireIMessageArchive(cfg); err != nil {
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

			run, err := imessage.Run(cmd.Context(), st, imessage.Options{
				ArchiveRoot: cfg.IMessageArchiveRoot,
				Full:        full,
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(),
				"imessage-import: %d/%d conversations changed, %d messages total (%d added), %d skipped lines in %dms\n",
				run.ConversationsChanged, run.ConversationsScanned, run.MessagesTotal, run.MessagesAdded,
				run.SkippedLines, run.DurationMS)
			return err
		},
	}
	cmd.Flags().Bool("full", false, "ignore incremental state and re-scan every conversation")
	return cmd
}
