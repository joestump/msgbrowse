// Command probe is the CI relocation regression guard for the bundled exporter
// toolchain (issue #147). It takes a built msgbrowse.app path and version-probes
// every bundled tool using the EXACT resolver + env logic the shipped app runs
// at runtime (internal/toolchain.VerifyTool with a nil runner, which applies the
// relocation-corrected PYTHONHOME/PYTHONPATH env for the Python tools and inherits
// the environment for the native imessage-exporter).
//
// The point is to run this from a RELOCATED copy of the .app (a path different
// from the CI build path — e.g. /tmp/relocated/msgbrowse.app), reproducing the
// /Applications move on a user's Mac. Before the relocation fix the bundled
// python-build-standalone interpreter fell back to its compiled-in `/install`
// prefix and died with "ModuleNotFoundError: No module named 'encodings'"; this
// probe FAILS in that state and PASSES once PYTHONHOME points at the bundled
// python home. Because it calls the SAME VerifyTool the app uses, the CI guard
// and production cannot drift.
//
// Usage:
//
//	go run ./internal/toolchain/probe /tmp/relocated/msgbrowse.app
//
// It exits non-zero (with the per-tool error) if any tool fails to version-probe,
// so a CI step can simply run it and assert success.
//
// It is pure Go (no cgo, no desktop build tag) so `go run` works on the macOS
// runner without the Wails/webview toolchain, and it is compiled by the desktop
// module's headless `CGO_ENABLED=0 go build ./...`.
//
// Governing: ADR-0020 (bundled exporter toolchain), SPEC-0013 REQ "Bundled tool
// integrity and version check", issue #147 (relocation regression guard).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/joestump/msgbrowse/cmd/msgbrowse-desktop/internal/toolchain"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <path-to-msgbrowse.app>\n", filepath.Base(os.Args[0]))
		os.Exit(2)
	}
	appPath := os.Args[1]

	// Simulate os.Executable() from inside the .app: Contents/MacOS/msgbrowse.
	// Locate derives Contents/Resources/tools from this exactly as the shipped
	// binary does at runtime, so the probe resolves the SAME bundled paths and
	// the SAME PYTHONHOME/PYTHONPATH env.
	exe := filepath.Join(appPath, "Contents", "MacOS", "msgbrowse")
	r, err := toolchain.Locate(exe)
	if err != nil {
		if errors.Is(err, toolchain.ErrNotBundled) {
			fmt.Fprintf(os.Stderr, "not a .app bundle: %s\n", appPath)
		} else {
			fmt.Fprintf(os.Stderr, "locate bundled toolchain: %v\n", err)
		}
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Verify every bundled tool under the resolver's real env logic. A nil runner
	// means VerifyTool applies the corrected env per tool and runs the real
	// version probe — the whole reason this reproduces the relocation bug.
	failed := false
	for _, t := range toolchain.AllTools {
		info, verr := r.VerifyTool(ctx, t, nil)
		if verr != nil {
			failed = true
			fmt.Fprintf(os.Stderr, "FAIL  %-18s %v\n", toolchain.Name(t), verr)
			continue
		}
		fmt.Printf("OK    %-18s %s\n", info.Name, info.Version)
	}
	if failed {
		fmt.Fprintln(os.Stderr, "\nbundled toolchain relocation probe FAILED — a bundled tool did not run from the relocated .app")
		os.Exit(1)
	}
	fmt.Println("\nbundled toolchain relocation probe PASSED")
}
