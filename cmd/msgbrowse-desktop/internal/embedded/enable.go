// Wiring the Setup Enable flow (SPEC-0013) into the desktop embedded server
// with the BUNDLED exporter toolchain. This is where the .app resolves
// exporters from Contents/Resources and NEVER from $PATH: the ToolResolver here
// calls internal/toolchain.ResolveExporter (per-source) at a LIVE export site,
// closing the anti-$PATH-fallback guarantee that was latent until an actual
// Enable resolved through the bundle.
//
// Per-source resolution is deliberate (issue #147): each source integrity-checks
// ONLY its own exporter, so a broken bundled Python / sigexport never blocks an
// iMessage enable (iMessage depends solely on the native imessage-exporter). The
// resolver also implements onboard.EnvResolver, handing the export-spawn path the
// relocation-corrected PYTHONHOME/PYTHONPATH env for the bundled Python exporters
// (and nil — inherit — for the Rust imessage-exporter), so a bundled Python tool
// runs after the .app is moved to /Applications.
//
// In the non-bundled Linux desktop build (ResolveExporter returns Bundled=false
// with empty paths), the resolver falls back to $PATH exactly as `msgbrowse
// serve` does — a dev run still works with BYO exporters — while the real signed
// .app always resolves the verified bundled absolute paths.
//
// Governing: ADR-0020 (bundled exporter toolchain — the desktop app resolves
// bundled paths directly and never reads $PATH), SPEC-0013 REQ "Bundled
// toolchain resolution", REQ "One-click enable and import per source", issue #147
// (relocatable bundled Python + iMessage decoupling).
package embedded

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/joestump/msgbrowse/cmd/msgbrowse-desktop/internal/toolchain"
	"github.com/joestump/msgbrowse/internal/config"
	"github.com/joestump/msgbrowse/internal/onboard"
	"github.com/joestump/msgbrowse/internal/onboardsvc"
	"github.com/joestump/msgbrowse/internal/source"
	"github.com/joestump/msgbrowse/internal/store"
	"github.com/joestump/msgbrowse/internal/web"
)

// wireEnable builds the onboard.Runner backed by the bundled-toolchain resolver
// and wires it into the web server's Setup Enable flow. It returns the runner so
// Start can tear its workers down on Close (SPEC-0013 REQ "Concurrency Safety":
// no exporter subprocess outlives the app). A construction failure is returned;
// the caller aborts Start rather than serving a half-wired app. opts pass
// through to onboardsvc.Build (Start threads the post-import embed trigger,
// issue #191).
func wireEnable(cfg *config.Config, st *store.Store, srv *web.Server, log *slog.Logger, opts ...onboardsvc.Option) (*onboard.Runner, error) {
	runner, err := onboardsvc.Build(cfg, st, bundledResolver{}, log, opts...)
	if err != nil {
		return nil, fmt.Errorf("wire setup enable: %w", err)
	}
	srv.SetEnabler(runner)
	return runner, nil
}

// bundledResolver resolves each source's exporter through the bundled toolchain,
// making internal/toolchain.ResolveExporter live at the export path. In a macOS
// .app it returns the verified bundled absolute path FOR THAT SOURCE ONLY; in the
// non-bundled build it returns the empty override so the resolver falls back to
// $PATH (the same BYO behavior as serve). A corrupt bundle surfaces the toolchain
// error, which onboard maps to a per-source Enable failure.
//
// It resolves ONLY the requested source's own tool (issue #147): an iMessage
// enable integrity-checks only imessage-exporter (Rust), so a broken bundled
// Python / sigexport can never block iMessage. It also implements
// onboard.EnvResolver so the export-spawn path runs a bundled Python exporter
// under the relocation-corrected PYTHONHOME/PYTHONPATH env — the same env the
// version probe already ran under, so the integrity check and the real run agree.
type bundledResolver struct{}

// ResolveTool implements onboard.ToolResolver. It resolves and integrity-checks
// JUST this source's exporter (toolchain.ResolveExporter), decoupling each
// source from the others (issue #147).
func (bundledResolver) ResolveTool(ctx context.Context, src string) (string, error) {
	if !source.IsKnown(src) {
		return "", fmt.Errorf("%w: %q", onboard.ErrUnknownSource, src)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable for bundled toolchain: %w", err)
	}
	// nil runner => real env-aware process version probe (production).
	res, err := toolchain.ResolveExporter(ctx, exe, src, nil)
	if err != nil {
		return "", err // corrupt bundle for THIS source: surfaced per-source by onboard
	}
	if !res.Bundled || res.Path == "" {
		// Non-bundled build: fall back to $PATH exactly as the bring-your-own CLI
		// does, so a dev/Linux desktop run still enables sources with tools on PATH.
		return onboardsvc.PathToolResolver{}.ResolveTool(ctx, src)
	}
	return res.Path, nil
}

// EnvForTool implements onboard.EnvResolver: it returns the subprocess
// environment for the resolved tool. In a macOS .app this is the corrected
// PYTHONHOME/PYTHONPATH env for a bundled Python exporter (Signal, WhatsApp) and
// nil for the native imessage-exporter (issue #147); in the non-bundled build it
// is nil so the $PATH-resolved BYO tool inherits the environment. toolPath is
// what ResolveTool returned, used to confirm the resolution is still the bundled
// path before applying a bundled env.
func (bundledResolver) EnvForTool(ctx context.Context, src, toolPath string) ([]string, error) {
	if !source.IsKnown(src) {
		return nil, fmt.Errorf("%w: %q", onboard.ErrUnknownSource, src)
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable for bundled toolchain: %w", err)
	}
	res, err := toolchain.ResolveExporter(ctx, exe, src, nil)
	if err != nil {
		return nil, err
	}
	// Non-bundled, or the resolved path is a $PATH fallback (not the bundled
	// path): inherit the environment — never hand a $PATH tool a bundled env.
	if !res.Bundled || res.Path == "" || res.Path != toolPath {
		return nil, nil
	}
	return res.Env, nil
}
