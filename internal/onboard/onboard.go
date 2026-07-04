// Package onboard is the shared, UI-free orchestration for the desktop guided-
// setup Enable flow (SPEC-0013): resolve the exporter for a source, run it into
// a STAGING directory, atomically adopt the staged output into the app-owned
// managed archive root, then import it into the store — reporting structured
// progress throughout and honoring cancellation at every step.
//
// It is pure Go with every side effect injected: the exporter tool paths come
// from a ToolResolver (the desktop shell backs it with the bundled toolchain;
// `msgbrowse serve` backs it with $PATH), the subprocess spawn comes from an
// ExecRunner, and the import comes from an Importer. That injection is the whole
// point — the Enable→export→adopt→import pipeline is driven end to end on Linux
// with a FAKE exporter (a func that writes a known fixture archive) and NO cgo,
// so the concurrency, staging/atomic-adopt, cancellation, and error-handling
// contracts are all covered by `go test ./internal/onboard`.
//
// Concurrency Safety (SPEC-0013 REQ): the Runner is a supervised worker registry
// keyed by source. Exactly one mutating job runs per source at a time; a second
// Enable/Refresh while one is in flight is rejected with ErrJobInProgress rather
// than spawning a duplicate exporter. All shared state (the per-source job map)
// is guarded by a mutex, and each job carries its own cancellable context so a
// Cancel — or app shutdown — tears the exporter subprocess down promptly.
//
// Error Handling Standards (SPEC-0013 REQ): every failure mode has a sentinel
// (ErrToolMissing, ErrPermissionDenied, ErrExportFailed, ErrImportFailed,
// ErrCancelled) wrapped with context; the staging + atomic-adopt design means a
// cancelled or failed export NEVER promotes partial output into the managed root
// (the store and archive are left import-clean), and errors are surfaced as
// structured job state, never swallowed.
//
// Governing: ADR-0020 (self-contained desktop onboarding — the export jobs drive
// the bundled exporter into the managed archives, then the existing importer
// loads the store), SPEC-0013 REQ "One-click enable and import per source", REQ
// "Error Handling Standards", REQ "Concurrency Safety", and §Security (the
// exporter argv is assembled from app-owned constants + the bundled tool path +
// the computed managed root — no client input reaches the command line).
package onboard

import (
	"context"
	"errors"
	"fmt"

	"github.com/joestump/msgbrowse/internal/source"
)

// Sentinel errors for the known failure modes SPEC-0013 REQ "Error Handling
// Standards" names. Callers (the web layer, tests) match these with errors.Is
// to render a precise per-source message rather than a generic failure.
var (
	// ErrToolMissing: the exporter for the source could not be resolved (no
	// bundled tool and none on $PATH), so no subprocess can be spawned.
	ErrToolMissing = errors.New("onboard: exporter tool not available for source")
	// ErrPermissionDenied: an OS consent gate (macOS Full Disk Access / Signal
	// Keychain / WhatsApp container) is not granted, so the export cannot read
	// the source. Surfaced as guidance, not a hard crash.
	ErrPermissionDenied = errors.New("onboard: os permission not granted for source")
	// ErrExportFailed: the exporter subprocess exited non-zero (e.g. a locked
	// source database). The staged output is discarded; the managed root is
	// untouched.
	ErrExportFailed = errors.New("onboard: exporter failed")
	// ErrImportFailed: the export succeeded and was adopted, but importing the
	// adopted archive into the store failed.
	ErrImportFailed = errors.New("onboard: import failed")
	// ErrCancelled: the caller cancelled the job (or the app is shutting down)
	// before it completed. No partial output is promoted.
	ErrCancelled = errors.New("onboard: job cancelled")
	// ErrJobInProgress: a mutating job for this source is already running; a
	// second Enable/Refresh is rejected rather than spawning a duplicate exporter
	// (SPEC-0013 REQ "Concurrency Safety" — "A second Enable while one is running
	// is rejected").
	ErrJobInProgress = errors.New("onboard: a job is already running for this source")
	// ErrUnknownSource: the source id is not one of the fixed enum values. The web
	// layer rejects these before reaching the runner, but the orchestration guards
	// too so no client string can ever drive a filesystem path.
	ErrUnknownSource = errors.New("onboard: unknown source")
)

// Phase is the coarse step a job is in, for structured progress. It is reported
// as a stable lowercase token so the UI's aria-live region can announce it and
// tests can assert the lifecycle.
type Phase string

const (
	// PhaseQueued: the job is registered but has not started work yet.
	PhaseQueued Phase = "queued"
	// PhaseExporting: the exporter subprocess is running into the staging dir.
	PhaseExporting Phase = "exporting"
	// PhaseAdopting: the export finished; the staged output is being promoted
	// into the managed archive root.
	PhaseAdopting Phase = "adopting"
	// PhaseImporting: the adopted archive is being imported into the store.
	PhaseImporting Phase = "importing"
	// PhaseDone: the job finished successfully; the source is Enabled.
	PhaseDone Phase = "done"
	// PhaseFailed: the job ended in a terminal error (Err is set).
	PhaseFailed Phase = "failed"
	// PhaseCancelled: the job was cancelled before completion (Err wraps
	// ErrCancelled).
	PhaseCancelled Phase = "cancelled"
)

// Terminal reports whether a phase is an end state (no further transitions).
func (p Phase) Terminal() bool {
	return p == PhaseDone || p == PhaseFailed || p == PhaseCancelled
}

// ImportResult is the subset of an import run the UI announces: how many
// conversations and messages the incremental import added. It is deliberately
// small and importer-agnostic so the orchestration never depends on
// store.IngestRun.
type ImportResult struct {
	// ConversationsChanged is the number of conversations added or updated.
	ConversationsChanged int
	// MessagesAdded is the number of new messages written.
	MessagesAdded int
	// MessagesTotal is the store-wide message count after the import.
	MessagesTotal int
}

// ToolResolver resolves the exporter executable for one source. The desktop
// shell backs it with the bundled toolchain (verified, absolute Contents/
// Resources paths — never $PATH); `msgbrowse serve` backs it with a $PATH /
// config lookup; a build with no tool for a source returns ErrToolMissing so
// Enable is a clear "unavailable", never a silent no-op. The returned path is an
// explicit executable used verbatim as argv[0] — no shell, no PATH re-resolution
// inside the runner.
type ToolResolver interface {
	// ResolveTool returns the absolute executable path for src's exporter. A
	// source with no available tool returns ErrToolMissing (wrapped is fine —
	// callers match with errors.Is).
	ResolveTool(ctx context.Context, src string) (string, error)
}

// ToolResolverFunc adapts a plain func to ToolResolver (the common case: a
// closure over a resolver struct).
type ToolResolverFunc func(ctx context.Context, src string) (string, error)

// ResolveTool implements ToolResolver.
func (f ToolResolverFunc) ResolveTool(ctx context.Context, src string) (string, error) {
	return f(ctx, src)
}

// ExecRunner runs an exporter subprocess with an explicit argv and process
// environment, and blocks until it exits. It is the seam that makes the whole
// pipeline testable without the real macOS exporters: tests inject a fake that
// writes a fixture archive into the staging dir and returns nil (or a scripted
// error). Production wires it to a real exec.CommandContext runner (in the
// CLI/desktop layer, which owns the os/exec dependency).
//
// name is the resolved absolute tool path and args are app-owned constants plus
// the app-computed staging path — never client input (SPEC-0013 §Security
// "Subprocess argument safety"). env is the subprocess environment: nil means
// "inherit the parent process environment" (exec.Cmd.Env semantics), which is
// the case for a native exporter; a bundled Python exporter is handed the
// relocation-corrected PYTHONHOME/PYTHONPATH env so it can find its stdlib after
// the .app is moved (issue #147). The context MUST be honored so a Cancel kills
// the subprocess.
type ExecRunner func(ctx context.Context, name string, env []string, args ...string) error

// EnvResolver is an OPTIONAL capability a ToolResolver may also implement to
// supply the process environment a source's exporter subprocess must run with.
// The desktop's bundled resolver implements it to hand back the corrected
// PYTHONHOME/PYTHONPATH env for the Python exporters (Signal, WhatsApp) and nil
// (inherit) for the native imessage-exporter (issue #147). A resolver that does
// not implement it (e.g. the $PATH/BYO resolver `msgbrowse serve` uses) causes
// the runner to inherit the environment — the correct behavior for tools on
// $PATH. toolPath is the path ResolveTool just returned, so the resolver can map
// it back to the specific tool without re-resolving.
type EnvResolver interface {
	EnvForTool(ctx context.Context, src, toolPath string) ([]string, error)
}

// Importer imports an adopted managed archive root for a source into the store
// and returns the result. It is injected (rather than importing internal/ingest
// et al. directly) so the orchestration stays pure and off-store: the CLI /
// desktop layer, which holds the concrete *store.Store, supplies an Importer
// that dispatches to internal/ingest, internal/imessage, or internal/whatsapp by
// source. The archiveRoot is always the app-owned managed root — never a client
// path.
type Importer interface {
	// Import loads the source's managed archive root into the store incrementally
	// and returns what changed. A failure is wrapped by the runner as
	// ErrImportFailed.
	Import(ctx context.Context, src, archiveRoot string) (ImportResult, error)
}

// ImporterFunc adapts a plain func to Importer.
type ImporterFunc func(ctx context.Context, src, archiveRoot string) (ImportResult, error)

// Import implements Importer.
func (f ImporterFunc) Import(ctx context.Context, src, archiveRoot string) (ImportResult, error) {
	return f(ctx, src, archiveRoot)
}

// ExportArgs builds the exporter argv for a source, writing into the given
// destination directory. The destination is ALWAYS the app-computed staging dir
// (never the managed root directly, and never a client path): the exporter runs
// into staging and only a clean success is promoted. The flags mirror
// internal/cli/export.go's proven command lines exactly (iMessage always copy-
// mode `-c clone`; Signal writes into <dest>/export; WhatsApp writes JSON +
// media into <dest>), so the desktop and CLI export layouts cannot diverge.
//
// This is the single place the argv is assembled, and it is assembled only from
// app-owned constants + the tool path + the computed staging dir — no request
// input reaches it (SPEC-0013 §Security "Subprocess argument safety").
func ExportArgs(src, dest string) ([]string, error) {
	switch src {
	case source.Signal:
		// sigexport <dest>/export → <dest>/export/<conversation>/chat.md, the
		// layout internal/ingest scans (ingest.ExportDir).
		return []string{signalExportSubdir(dest)}, nil
	case source.IMessage:
		// imessage-exporter -f txt -c clone -o <dest>: copy-mode is mandatory so
		// attachments are cloned into the archive, not left as absolute ~/Library
		// references (the non-copy-mode trap doctor diagnoses).
		return []string{"-f", "txt", "-c", "clone", "-o", dest}, nil
	case source.WhatsApp:
		// wtsexporter -o <dest> -j <dest>/result.json: the JSON export and copied
		// media both land under <dest>, the layout internal/whatsapp scans. The
		// platform/input flags (-i/-a, -b/-d/-k) are the user's backup specifics
		// and are NOT part of the one-click Enable flow — a WhatsApp Enable that
		// needs them surfaces an export error the UI shows (SPEC-0013 error
		// handling), rather than guessing a backup location.
		return []string{"-o", dest, "-j", whatsappResultFile(dest)}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownSource, src)
	}
}
