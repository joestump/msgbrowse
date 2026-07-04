// Package toolchain resolves the exporter toolchain bundled inside the macOS
// .app under Contents/Resources/tools and verifies its integrity. The desktop
// app runs exports from these bundled paths and NEVER from $PATH: a fresh Mac
// with no Homebrew and no system Python must still export offline (ADR-0020
// option (d) "fully bundled"). The CLI keeps its bring-your-own-exporter path
// (internal/cli/export.go, PATH/override) — this package is the desktop-only
// provisioning layer.
//
// Bundle layout produced by the desktop CI matrix (.github/workflows/desktop.yml):
//
//	msgbrowse.app/
//	  Contents/
//	    MacOS/msgbrowse                       <- os.Executable() at runtime
//	    Resources/
//	      tools/
//	        python/bin/python3                <- relocatable python-build-standalone
//	        venv/bin/sigexport                <- signal-export console script
//	        venv/bin/wtsexporter              <- whatsapp-chat-exporter console script
//	        imessage-exporter                 <- native macOS binary
//
// The package is pure Go (no cgo, no Wails import, no `desktop` build tag) so it
// is exercised by the desktop module's `CGO_ENABLED=0 go test ./...` on headless
// Linux against a faked bundle layout — the real macOS paths never exist on the
// build box.
//
// Governing: ADR-0020 (self-contained desktop onboarding — bundled exporter
// toolchain), SPEC-0013 REQ "Bundled toolchain resolution", REQ "Bundled tool
// integrity and version check".
package toolchain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Tool identifies one bundled exporter (plus the Python runtime the two
// Python-based exporters run through). The zero value is not a valid Tool.
type Tool int

const (
	// Python is the relocatable interpreter (python-build-standalone) the venv
	// exporters execute under. Resolving it lets the About/Advanced view report
	// the bundled Python version and lets integrity checks confirm the runtime
	// the venv depends on is present.
	Python Tool = iota
	// Signal is signal-export's `sigexport` console script inside the venv.
	Signal
	// WhatsApp is whatsapp-chat-exporter's `wtsexporter` console script inside
	// the venv.
	WhatsApp
	// IMessage is the native imessage-exporter macOS binary.
	IMessage
)

// spec describes where a tool lives under the bundled tools dir and how to ask
// it for its version. relPath is joined onto the resolved tools directory;
// versionArgs is the flag that makes the tool print its version and exit 0.
type spec struct {
	name        string   // human label, used in errors and the About view
	relPath     string   // path relative to Contents/Resources/tools
	versionArgs []string // args that make the tool print its version and exit
}

// specs is the single source of truth for the bundle layout. The relative
// paths here MUST match what desktop.yml assembles into Contents/Resources/tools;
// the CI job and this table are the two halves of one contract.
//
// Version/liveness flags, chosen per tool from its real CLI (verified against
// the pinned upstream sources):
//   - python3 -V and imessage-exporter --version print a version and exit 0.
//   - sigexport (signal-export, a Typer CLI) supports --version and prints one.
//   - wtsexporter (whatsapp-chat-exporter, argparse) has NO --version flag, so
//     its liveness/integrity probe is --help (argparse auto-adds -h/--help and
//     exits 0). Its Version field then reflects the first --help line rather
//     than a bare version string — enough to confirm the bundled tool runs.
//
// The probe's job is twofold: confirm the bundled tool is present + executes
// (exit 0), and capture a human-readable identifier for the About view. A tool
// whose probe exits non-zero is reported as a bundled-tool error.
var specs = map[Tool]spec{
	Python:   {name: "python", relPath: filepath.Join("python", "bin", "python3"), versionArgs: []string{"-V"}},
	Signal:   {name: "sigexport", relPath: filepath.Join("venv", "bin", "sigexport"), versionArgs: []string{"--version"}},
	WhatsApp: {name: "wtsexporter", relPath: filepath.Join("venv", "bin", "wtsexporter"), versionArgs: []string{"--help"}},
	IMessage: {name: "imessage-exporter", relPath: "imessage-exporter", versionArgs: []string{"--version"}},
}

// toolsSubdir is the directory under Contents/Resources that holds the bundled
// toolchain. Exported-adjacent as a constant so the CI assembly step and the
// resolver agree on one name.
const toolsSubdir = "tools"

// ErrNotBundled is returned by Locate when the running binary is not inside a
// macOS .app bundle (e.g. the plain CLI, or the Linux/Windows desktop build,
// where bundling is deferred per ADR-0020 decision 3 "macOS-first"). Callers
// distinguish this from a corrupt bundle: not-bundled means "fall back to the
// CLI's PATH resolution"; a corrupt bundle is a hard error surfaced to Setup.
var ErrNotBundled = errors.New("not running from a macOS .app bundle")

// Resolver resolves bundled tool paths from a fixed tools directory. It holds
// no process-wide state and is safe to construct per call. The tools directory
// is injected so tests can point it at a faked bundle layout on Linux (the real
// Contents/Resources/tools never exists on the build box).
type Resolver struct {
	toolsDir string // absolute path to Contents/Resources/tools
}

// NewResolver builds a Resolver over an explicit tools directory. This is the
// testing seam: tests pass a faked-bundle tools dir; production uses Locate to
// derive the real one from os.Executable().
func NewResolver(toolsDir string) *Resolver {
	return &Resolver{toolsDir: toolsDir}
}

// Locate derives the bundled tools directory from the running executable's
// path and returns a Resolver over it. It expects the macOS .app layout:
//
//	.../Contents/MacOS/<exe>  ->  .../Contents/Resources/tools
//
// execPath is injected (pass os.Executable()'s result in production) so the
// derivation is unit-testable with a faked path on any OS. It returns
// ErrNotBundled when execPath is not inside a Contents/MacOS directory, so the
// caller can fall back to PATH resolution for the non-bundled CLI build.
func Locate(execPath string) (*Resolver, error) {
	if execPath == "" {
		return nil, fmt.Errorf("locate bundled tools: empty executable path")
	}
	// .../Contents/MacOS/msgbrowse -> macosDir=.../Contents/MacOS, contents=.../Contents
	macosDir := filepath.Dir(execPath)
	contents := filepath.Dir(macosDir)
	// Confirm the .app layout: the parent of the exe must be a "MacOS" dir whose
	// parent is a "Contents" dir. Anything else is not a bundle (CLI, Linux
	// build, `go run`), which is ErrNotBundled — not a corruption error.
	if filepath.Base(macosDir) != "MacOS" || filepath.Base(contents) != "Contents" {
		return nil, ErrNotBundled
	}
	toolsDir := filepath.Join(contents, "Resources", toolsSubdir)
	return &Resolver{toolsDir: toolsDir}, nil
}

// ToolsDir returns the resolved bundled tools directory (absolute). Exposed for
// the About/Advanced view and for diagnostics.
func (r *Resolver) ToolsDir() string { return r.toolsDir }

// Path returns the absolute bundled path for a tool. It does NOT stat the file:
// it only joins the known relative path onto the resolved tools directory, so a
// caller can build a command line even while a separate integrity check reports
// a missing binary. It never consults $PATH. An unknown Tool is a programmer
// error and returns an error rather than a bogus path.
func (r *Resolver) Path(t Tool) (string, error) {
	s, ok := specs[t]
	if !ok {
		return "", fmt.Errorf("unknown bundled tool %d", int(t))
	}
	return filepath.Join(r.toolsDir, s.relPath), nil
}

// Name returns the human label for a tool (e.g. "sigexport"), for errors and
// the About view. An unknown Tool returns a placeholder rather than panicking.
func Name(t Tool) string {
	if s, ok := specs[t]; ok {
		return s.name
	}
	return fmt.Sprintf("tool#%d", int(t))
}

// pythonScripts are the bundled tools that are Python console scripts inside
// the venv. They resolve to a #!-shebanged script whose interpreter line points
// at the venv's python; because the bundled venv is relocatable, invoking the
// script by absolute path runs it under the bundled interpreter with no PATH
// lookup. imessage-exporter and python itself are native and not in this set.
var pythonScripts = map[Tool]bool{Signal: true, WhatsApp: true}

// SignalPath returns the bundled sigexport path, or ErrNotBundled-style errors
// bubbled from Path. Convenience wrappers keep call sites at the desktop export
// path readable and typed.
func (r *Resolver) SignalPath() (string, error)   { return r.Path(Signal) }
func (r *Resolver) IMessagePath() (string, error) { return r.Path(IMessage) }
func (r *Resolver) WhatsAppPath() (string, error) { return r.Path(WhatsApp) }

// Runner runs a tool binary with args and returns combined stdout+stderr. It is
// the seam that makes VerifyTool testable without the real macOS binaries: tests
// inject a fake that scripts version output or an error; production passes nil
// (VerifyTool/Verify default to execRunner). It carries a context so a wedged
// tool cannot hang startup.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// execRunner runs name+args and captures their combined output. The version
// probe is a trusted, argument-fixed invocation (no user input on the command
// line): name is a bundled absolute path and args are constants from specs.
func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// ToolError is the typed error surfaced when a bundled tool is missing, not
// executable, or fails to report a version. Setup renders it per source
// (SPEC-0013 REQ "Bundled tool integrity and version check": a missing or
// corrupt tool is a clear error state, never a PATH fallback and never a
// panic). It carries the tool identity so the UI can attribute the failure to
// the right source card.
type ToolError struct {
	Tool Tool   // which bundled tool
	Name string // its human label (specs[Tool].name)
	Path string // the absolute bundled path that failed
	Err  error  // the underlying cause (stat error, exec error, …)
}

func (e *ToolError) Error() string {
	return fmt.Sprintf("bundled tool %q at %s: %v", e.Name, e.Path, e.Err)
}

func (e *ToolError) Unwrap() error { return e.Err }

// ToolInfo is the verified state of one bundled tool: its resolved path and the
// version string it reported. The About/Advanced view lists these; Setup uses
// the presence of a ToolError (returned instead) to gate a source card.
type ToolInfo struct {
	Tool    Tool
	Name    string
	Path    string
	Version string // trimmed first line of the tool's --version output
}

// VerifyTool confirms one bundled tool exists, is a regular executable file,
// and reports a version. A missing file, a non-executable file, or a non-zero
// version probe each yield a *ToolError (never a panic) so Setup can surface a
// clear per-source error rather than silently falling back to PATH. On success
// it returns the tool's path and reported version.
//
// The nil runner defaults to execRunner; tests pass a fake to stay offline.
func (r *Resolver) VerifyTool(ctx context.Context, t Tool, run Runner) (ToolInfo, error) {
	if run == nil {
		run = execRunner
	}
	s, ok := specs[t]
	if !ok {
		return ToolInfo{}, fmt.Errorf("unknown bundled tool %d", int(t))
	}
	path := filepath.Join(r.toolsDir, s.relPath)
	info := ToolInfo{Tool: t, Name: s.name, Path: path}

	fi, err := os.Stat(path)
	if err != nil {
		return info, &ToolError{Tool: t, Name: s.name, Path: path, Err: err}
	}
	if fi.IsDir() {
		return info, &ToolError{Tool: t, Name: s.name, Path: path, Err: errors.New("expected an executable file, found a directory")}
	}
	// Executable bit check: a bundled tool that is present but not marked
	// executable would fail at spawn time — catch it here as a clear integrity
	// error. On the build box (Linux) the faked stubs set the bit; the real
	// bundle's binaries carry it from CI (chmod +x / codesign preserves it).
	if fi.Mode().Perm()&0o111 == 0 {
		return info, &ToolError{Tool: t, Name: s.name, Path: path, Err: errors.New("file is not executable")}
	}

	out, err := run(ctx, path, s.versionArgs...)
	if err != nil {
		return info, &ToolError{Tool: t, Name: s.name, Path: path, Err: fmt.Errorf("version probe failed: %w (output: %s)", err, strings.TrimSpace(string(out)))}
	}
	info.Version = firstLine(out)
	return info, nil
}

// AllTools is the fixed set of bundled tools, in a stable order for iteration
// (Python first — the runtime the venv scripts depend on — then the three
// exporters). Verify and the About view walk this slice.
var AllTools = []Tool{Python, Signal, WhatsApp, IMessage}

// Verify runs VerifyTool over every bundled tool and returns the collected
// ToolInfo for those that verified plus the *ToolError list for those that did
// not. It never returns a top-level error and never panics: an all-failed
// bundle yields an empty infos slice and four errs, which Setup renders as four
// broken source cards. Callers decide policy (any error blocks that source; the
// About view shows the versions that resolved).
//
// The nil runner defaults to execRunner; tests inject a fake.
func (r *Resolver) Verify(ctx context.Context, run Runner) (infos []ToolInfo, errs []*ToolError) {
	for _, t := range AllTools {
		info, err := r.VerifyTool(ctx, t, run)
		if err != nil {
			var te *ToolError
			if errors.As(err, &te) {
				errs = append(errs, te)
				continue
			}
			// A non-ToolError (unknown tool — impossible for AllTools) is wrapped
			// so the caller still sees a typed failure rather than a drop.
			errs = append(errs, &ToolError{Tool: t, Name: Name(t), Err: err})
			continue
		}
		infos = append(infos, info)
	}
	return infos, errs
}

// firstLine returns the trimmed first line of a tool's version output. Version
// flags print one line (e.g. "Python 3.12.7", "sigexport 1.9.0"); taking the
// first line guards against tools that append a newline or extra diagnostics.
func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// IsPythonScript reports whether a tool is a venv console script (Signal,
// WhatsApp) rather than a native binary (Python, IMessage). The desktop export
// path uses this only for documentation/diagnostics: because the bundled venv
// is relocatable, every tool — script or native — is invoked by its absolute
// bundled path with no PATH lookup, so no interpreter prefix is needed.
func IsPythonScript(t Tool) bool { return pythonScripts[t] }

// --- desktop export-path wiring ---------------------------------------------

// ExporterPaths are the resolved exporter executables for one export run,
// suitable for the CLI export orchestration's bin-override fields
// (--signal-export-bin / --imessage-exporter-bin / --whatsapp-exporter-bin, or
// the matching config keys internal/cli.resolveBin reads). Empty strings mean
// "no override" — the CLI then looks the default name up on $PATH, which is the
// correct behavior for the NON-bundled build only (the plain CLI, the Linux
// desktop build, or a dev `go run`). In a macOS .app the fields are absolute
// bundled paths and $PATH is never consulted.
type ExporterPaths struct {
	// Bundled reports whether these paths came from the .app bundle. When true,
	// every exporter field is an absolute bundled path and $PATH MUST NOT be
	// consulted; when false, the fields are empty and the caller falls back to
	// $PATH (ADR-0020: the CLI/BYO path is unchanged; only the .app bundles).
	Bundled bool

	Signal   string // bundled sigexport, or "" (fall back to PATH)
	IMessage string // bundled imessage-exporter, or ""
	WhatsApp string // bundled wtsexporter, or ""
}

// ResolveExporters resolves the exporter paths for the running desktop app from
// its executable path:
//
//   - In a macOS .app (Locate succeeds): the bundle's integrity is verified
//     first — a missing or non-executable tool returns the representative typed
//     *ToolError so the caller (Setup) surfaces a clear per-source error instead
//     of silently falling back to $PATH — then every exporter is returned as its
//     bundled absolute path. No $PATH lookup happens in this branch.
//   - Not in a .app (ErrNotBundled — the Linux desktop build or `go run`):
//     returns ExporterPaths{Bundled:false} with empty fields, so the CLI export
//     path falls back to $PATH exactly as the bring-your-own CLI does.
//
// execPath is injected (pass os.Executable()'s result in production) so the
// whole function is unit-testable on Linux with a faked bundle. run is the
// version-probe seam: pass nil in production to use the real process runner.
func ResolveExporters(ctx context.Context, execPath string, run Runner) (ExporterPaths, error) {
	r, err := Locate(execPath)
	if err != nil {
		if errors.Is(err, ErrNotBundled) {
			// The ONLY branch that permits a later $PATH lookup, and only because
			// this build is not a bundle.
			return ExporterPaths{Bundled: false}, nil
		}
		return ExporterPaths{}, err
	}

	// Bundled: verify integrity before handing back paths. A broken bundle is a
	// hard, typed error — we never degrade to $PATH here (that would defeat the
	// offline, pinned-version guarantee and could run an unexpected tool).
	if _, errs := r.Verify(ctx, run); len(errs) > 0 {
		return ExporterPaths{}, errs[0]
	}

	sig, err := r.SignalPath()
	if err != nil {
		return ExporterPaths{}, err
	}
	im, err := r.IMessagePath()
	if err != nil {
		return ExporterPaths{}, err
	}
	wa, err := r.WhatsAppPath()
	if err != nil {
		return ExporterPaths{}, err
	}
	return ExporterPaths{Bundled: true, Signal: sig, IMessage: im, WhatsApp: wa}, nil
}
