//go:build desktop

// Desktop wiring for the bundled exporter toolchain. All resolution and
// integrity logic lives in the pure-Go, Linux-tested internal/toolchain
// package; this file is the thin desktop-tagged seam that feeds it the running
// binary's real path via os.Executable(). Keeping the logic in the untagged
// package means the whole export-path resolution is covered by the desktop
// module's CGO_ENABLED=0 headless suite — this file adds only the os.Executable
// call the tests deliberately inject around.
//
// The desktop Setup surface (a separate story in the onboarding epic, #129)
// calls resolveBundledExporters to obtain the exporter paths before spawning an
// export: in a macOS .app it gets verified, bundled absolute paths (never
// $PATH); in the non-bundled Linux build it gets empty overrides so the export
// orchestration falls back to $PATH exactly as the bring-your-own CLI does
// (ADR-0020: only the .app bundles; the CLI is unchanged).
//
// Governing: ADR-0020 (bundled exporter toolchain + guided setup), SPEC-0013
// REQ "Bundled toolchain resolution", REQ "Bundled tool integrity and version
// check".
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/joestump/msgbrowse/cmd/msgbrowse-desktop/internal/toolchain"
)

// resolveBundledExporters resolves the exporter binaries for the running
// desktop app from the .app bundle, verifying integrity first. In a macOS .app
// the returned ExporterPaths carry verified bundled absolute paths (Bundled
// true); outside a bundle they are empty (Bundled false) so the export
// orchestration falls back to $PATH. A corrupt bundle returns a typed
// *toolchain.ToolError the Setup UI surfaces per source.
//
// This is the seam the Setup surface (onboarding epic #129) calls before
// spawning an export; it is kept here, desktop-tagged, so the os.Executable()
// call stays out of the Linux-tested resolver package.
func resolveBundledExporters(ctx context.Context) (toolchain.ExporterPaths, error) {
	exe, err := os.Executable()
	if err != nil {
		return toolchain.ExporterPaths{}, err
	}
	// nil runner => real process version probe (production).
	return toolchain.ResolveExporters(ctx, exe, nil)
}

// logBundledToolchain runs the bundled-tool integrity + version check once at
// startup and logs the outcome, recording each tool's pinned version for the
// About/Advanced view and warning (never crashing) on a corrupt bundle. It is a
// no-op in the non-bundled build (Locate returns ErrNotBundled), so dev runs
// and the Linux desktop build stay quiet. The check is time-boxed so a wedged
// bundled binary cannot stall launch.
func logBundledToolchain(ctx context.Context, log *slog.Logger) {
	exe, err := os.Executable()
	if err != nil {
		log.Warn("could not resolve executable for bundled-toolchain check", "error", err)
		return
	}
	r, err := toolchain.Locate(exe)
	if err != nil {
		if errors.Is(err, toolchain.ErrNotBundled) {
			// Non-bundled build: exporters come from $PATH via the CLI path. Nothing
			// to verify, nothing to log at info level.
			log.Debug("desktop toolchain: not a macOS .app bundle; exporters resolve via PATH")
			return
		}
		log.Warn("could not locate bundled toolchain", "error", err)
		return
	}

	vctx, cancel := context.WithTimeout(ctx, bundledVerifyTimeout)
	defer cancel()
	infos, errs := r.Verify(vctx, nil)
	for _, in := range infos {
		log.Info("bundled tool verified", "tool", in.Name, "version", in.Version, "path", in.Path)
	}
	for _, e := range errs {
		// A per-tool error is surfaced later by Setup as a broken source card; at
		// startup it is a warning, and the app keeps running.
		log.Warn("bundled tool failed integrity check", "tool", e.Name, "path", e.Path, "error", e.Err)
	}
}

// bundledVerifyTimeout bounds the startup integrity check so a hung bundled
// binary (e.g. a broken relocatable Python) reads as a per-tool failure instead
// of blocking the window from opening.
const bundledVerifyTimeout = 10 * time.Second
