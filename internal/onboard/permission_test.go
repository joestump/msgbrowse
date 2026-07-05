package onboard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/joestump/msgbrowse/internal/source"
)

// TestClassifyExportFailure drives the permission classifier over realistic
// exporter output (synthetic paths only — never real user paths/content),
// including negative cases: a generic failure must stay ErrExportFailed so an
// unrelated crash never masquerades as a permission problem (issue #174).
func TestClassifyExportFailure(t *testing.T) {
	cases := []struct {
		name       string
		output     string
		permission bool
	}{
		{
			// The real-Mac stale-FDA shape: imessage-exporter names Full Disk
			// Access explicitly after SQLite refuses the open.
			name: "imessage-exporter full disk access hint",
			output: "Invalid configuration: Unable to read from chat database: unable to open database file: " +
				"/Users/testuser/Library/Messages/chat.db Ensure full disk access is enabled for your terminal " +
				"emulator in System Settings > Privacy & Security > Full Disk Access",
			permission: true,
		},
		{
			// SQLite's bare open error, without the FDA hint around it.
			name:       "sqlite unable to open database file",
			output:     "Error: unable to open database file: /Users/testuser/Library/Messages/chat.db",
			permission: true,
		},
		{
			// The raw EPERM text a TCC-blocked filesystem call surfaces.
			name:       "operation not permitted",
			output:     "open /Users/testuser/Library/Messages/chat.db: operation not permitted",
			permission: true,
		},
		{
			name:       "authorization denied",
			output:     "sigexport: Authorization denied while reading the Signal key",
			permission: true,
		},
		{
			// Case-insensitive: an exporter that SHOUTS still classifies.
			name:       "uppercase full disk access",
			output:     "FATAL: GRANT FULL DISK ACCESS AND RETRY",
			permission: true,
		},
		{
			// The WhatsApp argparse exit-2 shape is a config error, not permission.
			name:       "wtsexporter argparse usage stays generic",
			output:     "usage: wtsexporter [-h] ...\nwtsexporter: error: iOS mode requires -d",
			permission: false,
		},
		{
			name:       "generic crash stays generic",
			output:     "panic: runtime error: index out of range [3] with length 2",
			permission: false,
		},
		{
			name:       "empty output stays generic",
			output:     "",
			permission: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyExportFailure(tc.output)
			if tc.permission && !errors.Is(got, ErrPermissionDenied) {
				t.Errorf("classifyExportFailure(%q) = %v, want ErrPermissionDenied", tc.output, got)
			}
			if !tc.permission && !errors.Is(got, ErrExportFailed) {
				t.Errorf("classifyExportFailure(%q) = %v, want ErrExportFailed", tc.output, got)
			}
		})
	}
}

// TestExportPermissionShapedFailure drives the runner end to end with an
// exporter that dies on the permission wall (the stale-FDA shape from issue
// #174): the terminal error must wrap ErrPermissionDenied — so the UI re-enters
// the guidance flow — while preserving the original exit error and the raw
// captured output in the JobLog. Both Enable and Refresh share start(), so both
// entry points are exercised.
func TestExportPermissionShapedFailure(t *testing.T) {
	const fdaStderr = "Unable to read from chat database: unable to open database file: " +
		"/Users/testuser/Library/Messages/chat.db Ensure full disk access is enabled"
	exitErr := errors.New("exit status 1")
	starts := []struct {
		name  string
		start func(r *Runner) (Progress, error)
	}{
		{"enable", func(r *Runner) (Progress, error) { return r.Enable(source.IMessage) }},
		{"refresh", func(r *Runner) (Progress, error) { return r.Refresh(source.IMessage) }},
	}
	for _, tc := range starts {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewRunner(Config{
				Resolver: staticResolver("/bundle/imessage-exporter"),
				Exec: func(ctx context.Context, name string, env []string, args ...string) (string, error) {
					return fdaStderr, exitErr
				},
				Importer: ImporterFunc(func(ctx context.Context, src, root string) (ImportResult, error) {
					t.Error("importer must not run after a failed export")
					return ImportResult{}, nil
				}),
				DataDir: t.TempDir(),
			})
			if err != nil {
				t.Fatalf("NewRunner: %v", err)
			}
			defer r.Shutdown()

			if _, err := tc.start(r); err != nil {
				t.Fatalf("start: %v", err)
			}
			term := waitFor(t, r, source.IMessage, func(p Progress) bool { return p.Phase.Terminal() })
			if term.Phase != PhaseFailed {
				t.Fatalf("expected PhaseFailed, got %q (err=%v)", term.Phase, term.Err)
			}
			if !errors.Is(term.Err, ErrPermissionDenied) {
				t.Fatalf("terminal error %v does not wrap ErrPermissionDenied", term.Err)
			}
			// The original exit error survives the classification wrap.
			if !errors.Is(term.Err, exitErr) {
				t.Fatalf("terminal error %v lost the original exit error", term.Err)
			}
			// The raw exporter output stays on the JobLog for the Logs viewer,
			// exactly as a generic failure records it.
			if term.Log.Output != fdaStderr {
				t.Fatalf("JobLog.Output = %q, want the captured exporter stderr", term.Log.Output)
			}
			if term.Log.ExitStatus != exitErr.Error() {
				t.Fatalf("JobLog.ExitStatus = %q, want %q", term.Log.ExitStatus, exitErr.Error())
			}
		})
	}
}

// TestExportSuccessWithPermissionTextStaysSuccess: classification applies ONLY
// to a non-zero exit — a clean exporter run whose chatter happens to contain a
// permission-shaped phrase is never reclassified into a failure.
func TestExportSuccessWithPermissionTextStaysSuccess(t *testing.T) {
	r, err := NewRunner(Config{
		Resolver: staticResolver("/bundle/sigexport"),
		Exec: func(ctx context.Context, name string, env []string, args ...string) (string, error) {
			dest := args[len(args)-1]
			convDir := filepath.Join(dest, "Alice")
			if err := os.MkdirAll(convDir, 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(convDir, "chat.md"),
				[]byte("[2022-01-01 10:00:00] Alice: hi\n"), 0o644); err != nil {
				return "", err
			}
			// A zero exit whose progress chatter mentions a permission phrase.
			return "note: skipped a file marked operation not permitted", nil
		},
		Importer: ImporterFunc(func(ctx context.Context, src, root string) (ImportResult, error) {
			return ImportResult{ConversationsChanged: 1, MessagesAdded: 1}, nil
		}),
		DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	defer r.Shutdown()

	if _, err := r.Enable(source.Signal); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	term := waitFor(t, r, source.Signal, func(p Progress) bool { return p.Phase.Terminal() })
	if term.Phase != PhaseDone {
		t.Fatalf("expected PhaseDone for a zero-exit export, got %q (err=%v)", term.Phase, term.Err)
	}
}
