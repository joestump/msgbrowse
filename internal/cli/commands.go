package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

// errNotImplemented marks subcommands whose behavior lands in a later vertical
// slice. The command tree, flags, and config wiring are real today so the binary
// builds and the surface is stable.
var errNotImplemented = errors.New("not implemented yet (tracked in the project TODO)")

func newWatchCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "watch",
		Short: "Re-ingest automatically when the archive changes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := resolveConfig(); err != nil {
				return err
			}
			return errNotImplemented
		},
	}
}

func newJournalCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "journal",
		Short: "Rebuild the day-by-day journal and optional LLM digests",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := resolveConfig(); err != nil {
				return err
			}
			return errNotImplemented
		},
	}
	cmd.Flags().String("since", "", "only process days on or after this date (YYYY-MM-DD)")
	cmd.Flags().Bool("backfill", false, "process all days that lack a current digest")
	cmd.Flags().Bool("regenerate", false, "regenerate digests even if cached")
	cmd.Flags().Bool("dry-run", false, "print day count and cost estimate; make no LLM calls")
	return cmd
}
