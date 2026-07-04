// Package onboardsvc wires the pure internal/onboard orchestration to the
// concrete side effects the running app owns — the SQLite store, the real
// exporter subprocess, and the source-appropriate importers — and exposes it as
// a web.Enabler. It is the single place both `msgbrowse serve` and the desktop
// shell construct the Setup Enable runner, so the two modes drive the identical
// export→adopt→import pipeline (SPEC-0013) and differ only in the injected
// ToolResolver (bundled toolchain in the .app; $PATH/config in serve).
//
// It is pure Go with no cgo and no Wails import, so it lives in internal/ and is
// importable by both the CLI (internal/cli) and the desktop embedded server
// (cmd/msgbrowse-desktop/internal/embedded).
//
// Governing: ADR-0020 (the export jobs drive the exporter into the managed
// archives, then the existing importer loads the store), SPEC-0013 REQ
// "One-click enable and import per source" (reuse the existing export
// orchestration + incremental import — neither reimplemented in the UI layer),
// §Security "Subprocess argument safety".
package onboardsvc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"

	"github.com/joestump/msgbrowse/internal/config"
	"github.com/joestump/msgbrowse/internal/imageconv"
	"github.com/joestump/msgbrowse/internal/imessage"
	"github.com/joestump/msgbrowse/internal/ingest"
	"github.com/joestump/msgbrowse/internal/onboard"
	"github.com/joestump/msgbrowse/internal/source"
	"github.com/joestump/msgbrowse/internal/store"
	"github.com/joestump/msgbrowse/internal/whatsapp"
)

// Build constructs the onboard.Runner that backs the web Setup Enable flow,
// wiring the pure orchestration to the concrete store, the real exporter
// subprocess runner, and the source-dispatching Importer. resolver decides where
// exporters come from: a bundled resolver (desktop .app) or a $PATH/config one
// (serve). A nil resolver leaves Enable disabled — the runner reports
// ErrToolMissing for every source, and the web layer renders the "unavailable"
// affordance.
//
// The returned *onboard.Runner satisfies web.Enabler directly; wire it with
// (*web.Server).SetEnabler and call Shutdown as part of graceful shutdown so no
// exporter subprocess outlives the app (SPEC-0013 REQ "Concurrency Safety").
func Build(cfg *config.Config, st *store.Store, resolver onboard.ToolResolver, log *slog.Logger) (*onboard.Runner, error) {
	if log == nil {
		log = slog.Default()
	}
	return onboard.NewRunner(onboard.Config{
		Resolver: resolver,
		Exec:     ExecRunner,
		Importer: &storeImporter{st: st, cfg: cfg, log: log},
		DataDir:  cfg.DataDir,
		Logger:   log,
	})
}

// ExecRunner spawns an exporter subprocess with an explicit argv and streams its
// stdout/stderr to the process's stdio (these exporters print useful progress).
// The context is honored so a Cancel or app shutdown terminates the child. name
// is a resolved absolute tool path and args are app-owned constants plus the
// app-computed staging dir — never client input (SPEC-0013 §Security "Subprocess
// argument safety").
func ExecRunner(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// storeImporter imports an adopted managed archive root into the store by
// dispatching to the source-appropriate importer — the same import code
// `msgbrowse import` runs, never reimplemented in the UI layer (SPEC-0013 REQ
// "the import MUST reuse the existing incremental ingest").
type storeImporter struct {
	st  *store.Store
	cfg *config.Config
	log *slog.Logger
}

// Import loads root into the store for src and returns the onboard-shaped result,
// then runs a best-effort media transcode (non-fatal, like `msgbrowse import`).
func (im *storeImporter) Import(ctx context.Context, src, root string) (onboard.ImportResult, error) {
	var run store.IngestRun
	var err error
	switch src {
	case source.Signal:
		run, err = ingest.Run(ctx, im.st, ingest.Options{ArchiveRoot: root})
	case source.IMessage:
		run, err = imessage.Run(ctx, im.st, imessage.Options{ArchiveRoot: root})
	case source.WhatsApp:
		run, err = whatsapp.Run(ctx, im.st, whatsapp.Options{ArchiveRoot: root})
	default:
		return onboard.ImportResult{}, fmt.Errorf("onboardsvc: unknown source %q", src)
	}
	if err != nil {
		return onboard.ImportResult{}, err
	}

	// Best-effort media transcode scoped to the source's managed root, so an
	// Enable only transcodes what it just imported. A missing converter falls
	// back to gallery placeholders (not an error).
	opts := imageconv.Options{DataDir: im.cfg.DataDir}
	switch src {
	case source.Signal:
		opts.ArchiveRoot = root
	case source.IMessage:
		opts.IMessageArchiveRoot = root
	case source.WhatsApp:
		opts.WhatsAppArchiveRoot = root
	}
	if _, cerr := imageconv.Run(ctx, im.st, opts); cerr != nil {
		im.log.Warn("onboardsvc: image transcode step failed; gallery may show placeholders", "source", src, "error", cerr)
	}

	return onboard.ImportResult{
		ConversationsChanged: run.ConversationsChanged,
		MessagesAdded:        run.MessagesAdded,
		MessagesTotal:        run.MessagesTotal,
	}, nil
}

// Default console command names looked up on $PATH when no override is given,
// mirroring internal/cli/export.go (the console script `sigexport`, not the pip
// package name; likewise `wtsexporter`).
const (
	defaultSignalExportBin     = "sigexport"
	defaultIMessageExporterBin = "imessage-exporter"
	defaultWhatsAppExporterBin = "wtsexporter"
)

// PathToolResolver resolves each source's exporter from an explicit override or
// $PATH — the bring-your-own-exporter path `msgbrowse serve` uses (ADR-0020:
// only the .app bundles; the CLI is unchanged). It returns onboard.ErrToolMissing
// when a source's tool is neither configured nor on PATH, so Enable is a clear
// "unavailable" rather than a silent no-op.
type PathToolResolver struct {
	SignalBin   string // explicit override; empty falls back to the default name on PATH
	IMessageBin string
	WhatsAppBin string
}

// PathResolverFromConfig builds a PathToolResolver from the exporter-bin config
// keys `msgbrowse export` already reads, so serve and the CLI resolve the same
// tools.
func PathResolverFromConfig(cfg *config.Config) PathToolResolver {
	return PathToolResolver{
		SignalBin:   cfg.SignalExportBin,
		IMessageBin: cfg.IMessageExporterBin,
		WhatsAppBin: cfg.WhatsAppExporterBin,
	}
}

// ResolveTool implements onboard.ToolResolver.
func (r PathToolResolver) ResolveTool(_ context.Context, src string) (string, error) {
	var override, defaultName string
	switch src {
	case source.Signal:
		override, defaultName = r.SignalBin, defaultSignalExportBin
	case source.IMessage:
		override, defaultName = r.IMessageBin, defaultIMessageExporterBin
	case source.WhatsApp:
		override, defaultName = r.WhatsAppBin, defaultWhatsAppExporterBin
	default:
		return "", fmt.Errorf("%w: %q", onboard.ErrUnknownSource, src)
	}
	if override != "" {
		return override, nil
	}
	path, err := exec.LookPath(defaultName)
	if err != nil {
		return "", fmt.Errorf("%w: %s not found on PATH", onboard.ErrToolMissing, defaultName)
	}
	return path, nil
}
