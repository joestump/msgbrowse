// Headless unit tests for the bundled-toolchain resolver and integrity check.
// They run with CGO_ENABLED=0 on Linux against a FAKED bundle layout built in a
// t.TempDir — the real macOS Contents/Resources/tools never exists on the build
// box, so the whole suite is table-driven with injected paths and a fake runner.
//
// Governing: ADR-0020 (bundled exporter toolchain), SPEC-0013 REQ "Bundled
// toolchain resolution", REQ "Bundled tool integrity and version check".
package toolchain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeBundle materializes a Contents/Resources/tools tree under a temp dir with
// executable stub files at each tool's expected relative path, and returns the
// tools dir. Stubs are empty executable files: enough for the stat + exec-bit
// checks; the version probe is driven by an injected fake runner, not real exec.
func fakeBundle(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	toolsDir := filepath.Join(root, "Contents", "Resources", toolsSubdir)
	for _, tl := range AllTools {
		s := specs[tl]
		p := filepath.Join(toolsDir, s.relPath)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", s.name, err)
		}
		writeExec(t, p)
	}
	return toolsDir
}

func writeExec(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", path, err)
	}
}

// TestLocateDerivesToolsDirFromExecutable checks the .app-layout derivation:
// .../Contents/MacOS/msgbrowse -> .../Contents/Resources/tools, and that
// non-bundle layouts return ErrNotBundled so the CLI can fall back to PATH.
func TestLocateDerivesToolsDirFromExecutable(t *testing.T) {
	base := filepath.Join("/Applications", "msgbrowse.app")
	cases := []struct {
		name     string
		exec     string
		wantErr  error
		wantTail string // expected suffix of ToolsDir when no error
	}{
		{
			name:     "canonical .app layout",
			exec:     filepath.Join(base, "Contents", "MacOS", "msgbrowse"),
			wantTail: filepath.Join("Contents", "Resources", "tools"),
		},
		{
			name:    "plain CLI binary, not bundled",
			exec:    filepath.Join("/usr", "local", "bin", "msgbrowse"),
			wantErr: ErrNotBundled,
		},
		{
			name:    "linux desktop build under build/bin",
			exec:    filepath.Join("/home", "u", "src", "build", "bin", "msgbrowse"),
			wantErr: ErrNotBundled,
		},
		{
			name:    "empty exec path",
			exec:    "",
			wantErr: nil, // distinct non-ErrNotBundled error asserted below
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Locate(c.exec)
			if c.exec == "" {
				if err == nil || errors.Is(err, ErrNotBundled) {
					t.Fatalf("empty exec: got err=%v, want a non-ErrNotBundled error", err)
				}
				return
			}
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got := r.ToolsDir(); filepath.Base(got) != "tools" || !hasSuffix(got, c.wantTail) {
				t.Errorf("ToolsDir = %q, want it to end with %q", got, c.wantTail)
			}
		})
	}
}

func hasSuffix(path, tail string) bool {
	return len(path) >= len(tail) && path[len(path)-len(tail):] == tail
}

// TestPathNeverConsultsPATH proves Path builds an absolute bundled path from the
// resolver's tools dir for each tool, matching the layout contract, and returns
// an error for an unknown tool rather than a bogus path.
func TestPathNeverConsultsPATH(t *testing.T) {
	toolsDir := filepath.Join("/x", "Contents", "Resources", "tools")
	r := NewResolver(toolsDir)
	want := map[Tool]string{
		Python:   filepath.Join(toolsDir, "python", "bin", "python3"),
		Signal:   filepath.Join(toolsDir, "venv", "bin", "sigexport"),
		WhatsApp: filepath.Join(toolsDir, "venv", "bin", "wtsexporter"),
		IMessage: filepath.Join(toolsDir, "imessage-exporter"),
	}
	for tl, exp := range want {
		got, err := r.Path(tl)
		if err != nil {
			t.Fatalf("Path(%v): %v", tl, err)
		}
		if got != exp {
			t.Errorf("Path(%v) = %q, want %q", tl, got, exp)
		}
	}
	if _, err := r.Path(Tool(999)); err == nil {
		t.Error("Path(unknown) = nil error, want an error")
	}
}

// TestVerifyToolReportsVersion drives the happy path: a present, executable
// stub whose fake runner prints a version yields a ToolInfo with the trimmed
// first line as Version and no error.
func TestVerifyToolReportsVersion(t *testing.T) {
	toolsDir := fakeBundle(t)
	r := NewResolver(toolsDir)

	// Keyed by the executable basename the fake runner sees (Python's bundled
	// binary is python3, per specs).
	versions := map[string]string{
		"python3":           "Python 3.12.7\n",
		"sigexport":         "sigexport, version 1.9.0\n",
		"wtsexporter":       "wtsexporter 0.12.0\n",
		"imessage-exporter": "imessage-exporter 3.0.0\nextra diagnostic line\n",
	}
	run := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		return []byte(versions[filepath.Base(name)]), nil
	}

	wantVer := map[Tool]string{
		Python:   "Python 3.12.7",
		Signal:   "sigexport, version 1.9.0",
		WhatsApp: "wtsexporter 0.12.0",
		IMessage: "imessage-exporter 3.0.0", // first line only
	}
	for _, tl := range AllTools {
		info, err := r.VerifyTool(context.Background(), tl, run)
		if err != nil {
			t.Fatalf("VerifyTool(%s): %v", Name(tl), err)
		}
		if info.Version != wantVer[tl] {
			t.Errorf("VerifyTool(%s).Version = %q, want %q", Name(tl), info.Version, wantVer[tl])
		}
		if info.Name != specs[tl].name {
			t.Errorf("VerifyTool(%s).Name = %q, want %q", Name(tl), info.Name, specs[tl].name)
		}
	}
}

// TestVerifyToolMissingBinary asserts an absent bundled tool yields a *ToolError
// (never a panic, never a PATH fallback), carrying the tool identity and path.
func TestVerifyToolMissingBinary(t *testing.T) {
	toolsDir := fakeBundle(t)
	// Delete the iMessage binary to simulate a corrupt/incomplete bundle.
	imessage := filepath.Join(toolsDir, specs[IMessage].relPath)
	if err := os.Remove(imessage); err != nil {
		t.Fatalf("remove stub: %v", err)
	}
	r := NewResolver(toolsDir)

	ranProbe := false
	run := func(context.Context, string, ...string) ([]byte, error) {
		ranProbe = true
		return nil, nil
	}
	_, err := r.VerifyTool(context.Background(), IMessage, run)
	if err == nil {
		t.Fatal("VerifyTool for a missing binary = nil error, want a ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("error type = %T, want *ToolError", err)
	}
	if te.Tool != IMessage || te.Name != "imessage-exporter" {
		t.Errorf("ToolError identity = {%v %q}, want {IMessage imessage-exporter}", te.Tool, te.Name)
	}
	if ranProbe {
		t.Error("version probe ran despite a missing binary; must fail before exec")
	}
}

// TestVerifyToolNonExecutable asserts a present-but-not-executable file is an
// integrity error, not a silent pass. The exec-bit is a real permission on the
// build box (Linux/macOS); skip on Windows where the bit is not modeled.
func TestVerifyToolNonExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix exec bit on windows")
	}
	toolsDir := fakeBundle(t)
	sig := filepath.Join(toolsDir, specs[Signal].relPath)
	if err := os.Chmod(sig, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	r := NewResolver(toolsDir)
	_, err := r.VerifyTool(context.Background(), Signal, func(context.Context, string, ...string) ([]byte, error) {
		t.Error("version probe ran despite a non-executable binary")
		return nil, nil
	})
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("error = %v (%T), want *ToolError for a non-executable file", err, err)
	}
}

// TestVerifyToolVersionProbeFails asserts a tool that exits non-zero on its
// version flag surfaces a *ToolError carrying the underlying exec error, never
// a panic.
func TestVerifyToolVersionProbeFails(t *testing.T) {
	toolsDir := fakeBundle(t)
	r := NewResolver(toolsDir)
	boom := errors.New("exit status 1")
	_, err := r.VerifyTool(context.Background(), Python, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Traceback..."), boom
	})
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("error = %v (%T), want *ToolError", err, err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("ToolError does not unwrap to the exec error: %v", err)
	}
}

// TestVerifyCollectsInfosAndErrors drives the aggregate: with two tools removed,
// Verify returns two ToolInfo (the survivors) and two *ToolError, in AllTools
// order, and never a top-level error.
func TestVerifyCollectsInfosAndErrors(t *testing.T) {
	toolsDir := fakeBundle(t)
	// Break Signal and WhatsApp (the two venv scripts).
	for _, tl := range []Tool{Signal, WhatsApp} {
		if err := os.Remove(filepath.Join(toolsDir, specs[tl].relPath)); err != nil {
			t.Fatalf("remove %s: %v", Name(tl), err)
		}
	}
	r := NewResolver(toolsDir)
	run := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		return []byte(filepath.Base(name) + " 1.0.0"), nil
	}
	infos, errs := r.Verify(context.Background(), run)
	if len(infos) != 2 {
		t.Fatalf("got %d infos, want 2 (python, imessage-exporter)", len(infos))
	}
	if len(errs) != 2 {
		t.Fatalf("got %d errs, want 2 (sigexport, wtsexporter)", len(errs))
	}
	// Survivors are Python and IMessage; failures are Signal and WhatsApp.
	gotOK := map[Tool]bool{}
	for _, in := range infos {
		gotOK[in.Tool] = true
	}
	if !gotOK[Python] || !gotOK[IMessage] {
		t.Errorf("survivors = %v, want python+imessage", gotOK)
	}
	gotErr := map[Tool]bool{}
	for _, e := range errs {
		gotErr[e.Tool] = true
	}
	if !gotErr[Signal] || !gotErr[WhatsApp] {
		t.Errorf("failures = %v, want signal+whatsapp", gotErr)
	}
}

// TestVerifyAllPresent is the fully-healthy bundle: every tool verifies, no
// errors — the shape Setup treats as "all sources have a working toolchain".
func TestVerifyAllPresent(t *testing.T) {
	r := NewResolver(fakeBundle(t))
	run := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		return []byte(filepath.Base(name) + " 9.9.9"), nil
	}
	infos, errs := r.Verify(context.Background(), run)
	if len(errs) != 0 {
		t.Fatalf("healthy bundle produced errors: %v", errs)
	}
	if len(infos) != len(AllTools) {
		t.Fatalf("got %d infos, want %d", len(infos), len(AllTools))
	}
}

// TestConvenienceWrappersMatchPath guards that SignalPath/IMessagePath/
// WhatsAppPath agree with Path — the desktop export path uses the wrappers.
func TestConvenienceWrappersMatchPath(t *testing.T) {
	r := NewResolver(filepath.Join("/b", "Contents", "Resources", "tools"))
	pairs := []struct {
		tool Tool
		fn   func() (string, error)
	}{
		{Signal, r.SignalPath},
		{IMessage, r.IMessagePath},
		{WhatsApp, r.WhatsAppPath},
	}
	for _, p := range pairs {
		want, _ := r.Path(p.tool)
		got, err := p.fn()
		if err != nil || got != want {
			t.Errorf("wrapper for %s = (%q,%v), want (%q,nil)", Name(p.tool), got, err, want)
		}
	}
}

// fakeBundleApp materializes a full .app layout (Contents/MacOS/msgbrowse plus
// Contents/Resources/tools with executable stubs) and returns the fake
// executable path — what os.Executable() would report inside the .app.
func fakeBundleApp(t *testing.T) (execPath string) {
	t.Helper()
	root := t.TempDir()
	app := filepath.Join(root, "msgbrowse.app")
	macos := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0o755); err != nil {
		t.Fatalf("mkdir MacOS: %v", err)
	}
	exe := filepath.Join(macos, "msgbrowse")
	writeExec(t, exe)
	toolsDir := filepath.Join(app, "Contents", "Resources", toolsSubdir)
	for _, tl := range AllTools {
		p := filepath.Join(toolsDir, specs[tl].relPath)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", specs[tl].name, err)
		}
		writeExec(t, p)
	}
	return exe
}

// TestResolveExportersBundled proves the .app path: a healthy bundle yields
// absolute bundled paths for all three exporters, Bundled=true, and never
// touches $PATH.
func TestResolveExportersBundled(t *testing.T) {
	exe := fakeBundleApp(t)
	run := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		return []byte(filepath.Base(name) + " 1.0.0"), nil
	}
	got, err := ResolveExporters(context.Background(), exe, run)
	if err != nil {
		t.Fatalf("ResolveExporters: %v", err)
	}
	if !got.Bundled {
		t.Fatal("Bundled = false in a .app, want true")
	}
	// Every exporter path must be absolute and sit under the bundle's tools dir.
	toolsDir := filepath.Join(filepath.Dir(filepath.Dir(exe)), "Resources", "tools")
	for _, p := range []string{got.Signal, got.IMessage, got.WhatsApp} {
		if !filepath.IsAbs(p) {
			t.Errorf("exporter path %q is not absolute", p)
		}
		rel, rerr := filepath.Rel(toolsDir, p)
		if rerr != nil || rel == ".." || hasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("exporter path %q is not under the bundled tools dir %q", p, toolsDir)
		}
	}
}

func hasPrefix(s, pre string) bool { return len(s) >= len(pre) && s[:len(pre)] == pre }

// TestResolveExportersNotBundled proves the fallback: outside a .app,
// ResolveExporters returns Bundled=false with empty fields so the CLI export
// path falls back to $PATH.
func TestResolveExportersNotBundled(t *testing.T) {
	got, err := ResolveExporters(context.Background(), filepath.Join("/usr", "local", "bin", "msgbrowse"), nil)
	if err != nil {
		t.Fatalf("ResolveExporters (non-bundled): %v", err)
	}
	if got.Bundled {
		t.Error("Bundled = true outside a .app, want false")
	}
	if got.Signal != "" || got.IMessage != "" || got.WhatsApp != "" {
		t.Errorf("non-bundled override paths must be empty, got %+v", got)
	}
}

// TestResolveExportersBrokenBundleIsTypedError proves a corrupt .app (a missing
// bundled tool) is a hard *ToolError — never a silent $PATH fallback.
func TestResolveExportersBrokenBundleIsTypedError(t *testing.T) {
	exe := fakeBundleApp(t)
	toolsDir := filepath.Join(filepath.Dir(filepath.Dir(exe)), "Resources", "tools")
	if err := os.Remove(filepath.Join(toolsDir, specs[Signal].relPath)); err != nil {
		t.Fatalf("remove sigexport stub: %v", err)
	}
	run := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		return []byte(filepath.Base(name) + " 1.0.0"), nil
	}
	_, err := ResolveExporters(context.Background(), exe, run)
	if err == nil {
		t.Fatal("broken bundle = nil error, want a ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("error type = %T, want *ToolError", err)
	}
	if te.Tool != Signal {
		t.Errorf("ToolError.Tool = %v, want Signal", te.Tool)
	}
}

// TestNameAndIsPythonScript covers the small classifiers used by errors, the
// About view, and the export-path documentation.
func TestNameAndIsPythonScript(t *testing.T) {
	if Name(Signal) != "sigexport" || Name(IMessage) != "imessage-exporter" {
		t.Errorf("Name mismatch: %q %q", Name(Signal), Name(IMessage))
	}
	if Name(Tool(1234)) == "" {
		t.Error("Name(unknown) must not be empty")
	}
	if !IsPythonScript(Signal) || !IsPythonScript(WhatsApp) {
		t.Error("Signal and WhatsApp must be python scripts")
	}
	if IsPythonScript(IMessage) || IsPythonScript(Python) {
		t.Error("IMessage and Python are native, not python scripts")
	}
}
