package cli

import (
	"fmt"

	"github.com/joestump/msgbrowse/internal/whatsapp"
	"github.com/spf13/cobra"
)

func newWhatsAppImportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "whatsapp-import",
		Short: "Import (or refresh) a WhatsApp-Chat-Exporter archive into the local store",
		Long: "whatsapp-import reads a read-only WhatsApp-Chat-Exporter JSON export\n" +
			"(result.json plus the media folders the tool copied, produced by\n" +
			"`wtsexporter --json`), parses each changed chat into the unified SQLite\n" +
			"store, and tags every row source=\"whatsapp\". Incremental and idempotent,\n" +
			"like signal-import and imessage-import.\n" +
			"\n" +
			"The path comes from whatsapp_archive_root /\n" +
			"MSGBROWSE_WHATSAPP_ARCHIVE_ROOT / --whatsapp-archive-root.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			if err := requireWhatsAppArchive(cfg); err != nil {
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

			run, err := whatsapp.Run(cmd.Context(), st, whatsapp.Options{
				ArchiveRoot: cfg.WhatsAppArchiveRoot,
				Full:        full,
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(),
				"whatsapp-import: %d/%d conversations changed, %d messages total (%d added), %d skipped entries in %dms\n",
				run.ConversationsChanged, run.ConversationsScanned, run.MessagesTotal, run.MessagesAdded,
				run.SkippedLines, run.DurationMS)
			return err
		},
	}
	cmd.Flags().Bool("full", false, "ignore incremental state and re-scan every conversation")
	return cmd
}
