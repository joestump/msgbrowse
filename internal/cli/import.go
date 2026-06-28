package cli

import (
	"fmt"
	"log/slog"

	"github.com/joestump/msgbrowse/internal/imageconv"
	"github.com/joestump/msgbrowse/internal/imessage"
	"github.com/joestump/msgbrowse/internal/ingest"
	"github.com/spf13/cobra"
)

func newImportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import every configured archive (Signal + iMessage)",
		Long: "import is the all-in-one importer: it runs signal-import and imessage-import\n" +
			"for whichever archive roots are configured (archive_root and/or\n" +
			"imessage_archive_root), into one database. A source whose root is unset is\n" +
			"skipped; a source whose root is set but missing is an error. It does NOT\n" +
			"embed (run `msgbrowse embed` separately — that step needs an LLM endpoint).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig()
			if err != nil {
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

			out := cmd.OutOrStdout()
			ran := 0

			if cfg.ArchiveRoot != "" {
				if err := requireArchive(cfg); err != nil {
					return err
				}
				run, err := ingest.Run(cmd.Context(), st, ingest.Options{ArchiveRoot: cfg.ArchiveRoot, Full: full})
				if err != nil {
					return fmt.Errorf("signal import: %w", err)
				}
				ran++
				fmt.Fprintf(out, "signal:   %d/%d conversations changed, %d messages total (%d added), %d skipped lines in %dms\n",
					run.ConversationsChanged, run.ConversationsScanned, run.MessagesTotal, run.MessagesAdded, run.SkippedLines, run.DurationMS)
			} else {
				slog.Info("skipping Signal: archive_root not set")
			}

			if cfg.IMessageArchiveRoot != "" {
				if err := requireIMessageArchive(cfg); err != nil {
					return err
				}
				run, err := imessage.Run(cmd.Context(), st, imessage.Options{ArchiveRoot: cfg.IMessageArchiveRoot, Full: full})
				if err != nil {
					return fmt.Errorf("imessage import: %w", err)
				}
				ran++
				fmt.Fprintf(out, "imessage: %d/%d conversations changed, %d messages total (%d added), %d skipped lines in %dms\n",
					run.ConversationsChanged, run.ConversationsScanned, run.MessagesTotal, run.MessagesAdded, run.SkippedLines, run.DurationMS)
			} else {
				slog.Info("skipping iMessage: imessage_archive_root not set")
			}

			if ran == 0 {
				return fmt.Errorf("nothing to import: set archive_root and/or imessage_archive_root (flags, config, or MSGBROWSE_* env)")
			}

			// Best-effort: transcode non-web images (HEIC/TIFF) so the gallery can
			// show them. A missing converter is fine — the UI falls back to
			// placeholders; run `msgbrowse media` later after installing one.
			if msum, cerr := imageconv.Run(cmd.Context(), st, imageconv.Options{
				ArchiveRoot:         cfg.ArchiveRoot,
				IMessageArchiveRoot: cfg.IMessageArchiveRoot,
				DataDir:             cfg.DataDir,
			}); cerr != nil {
				slog.Warn("image transcode step failed; gallery may show placeholders", "error", cerr)
			} else if !msum.NoConverter {
				fmt.Fprintf(out, "media:    %d transcoded, %d cached, %d source-missing, %d failed\n",
					msum.Converted, msum.Skipped, msum.Missing, msum.Failed)
			}
			return nil
		},
	}
	cmd.Flags().Bool("full", false, "ignore incremental state and re-scan every conversation")
	return cmd
}
