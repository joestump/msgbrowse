// Wiring the Setup Enable flow (SPEC-0013) into the desktop embedded server
// with the BUNDLED exporter toolchain. This is where the .app resolves
// exporters from Contents/Resources and NEVER from $PATH: the ToolResolver here
// calls internal/toolchain.ResolveExporters (the #139 resolver) at a LIVE export
// site, closing the anti-$PATH-fallback guarantee that was latent until an
// actual Enable resolved through the bundle.
//
// In the non-bundled Linux desktop build (ResolveExporters returns
// Bundled=false with empty paths), the resolver falls back to $PATH exactly as
// `msgbrowse serve` does — a dev run still works with BYO exporters — while the
// real signed .app always resolves the verified bundled absolute paths.
//
// Governing: ADR-0020 (bundled exporter toolchain — the desktop app resolves
// bundled paths directly and never reads $PATH), SPEC-0013 REQ "Bundled
// toolchain resolution", REQ "One-click enable and import per source".
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
// the caller aborts Start rather than serving a half-wired app.
func wireEnable(cfg *config.Config, st *store.Store, srv *web.Server, log *slog.Logger) (*onboard.Runner, error) {
	runner, err := onboardsvc.Build(cfg, st, bundledResolver{}, log)
	if err != nil {
		return nil, fmt.Errorf("wire setup enable: %w", err)
	}
	srv.SetEnabler(runner)
	return runner, nil
}

// bundledResolver resolves each source's exporter through the bundled toolchain,
// making internal/toolchain.ResolveExporters live at the export path. In a macOS
// .app it returns the verified bundled absolute path per source; in the
// non-bundled build it returns the empty override so the resolver falls back to
// $PATH (the same BYO behavior as serve). A corrupt bundle surfaces the toolchain
// error, which onboard maps to a per-source Enable failure.
type bundledResolver struct{}

// ResolveTool implements onboard.ToolResolver. It resolves the whole exporter
// set once (ResolveExporters verifies bundle integrity as a unit) and returns
// the requested source's path.
func (bundledResolver) ResolveTool(ctx context.Context, src string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable for bundled toolchain: %w", err)
	}
	// nil runner => real process version probe (production).
	paths, err := toolchain.ResolveExporters(ctx, exe, nil)
	if err != nil {
		return "", err // corrupt bundle: surfaced per-source by onboard
	}

	var path string
	switch src {
	case source.Signal:
		path = paths.Signal
	case source.IMessage:
		path = paths.IMessage
	case source.WhatsApp:
		path = paths.WhatsApp
	default:
		return "", fmt.Errorf("%w: %q", onboard.ErrUnknownSource, src)
	}

	if path == "" {
		// Non-bundled build (Bundled=false, empty paths): fall back to $PATH
		// exactly as the bring-your-own CLI does, so a dev/Linux desktop run still
		// enables sources with tools on PATH.
		return onboardsvc.PathToolResolver{}.ResolveTool(ctx, src)
	}
	return path, nil
}
