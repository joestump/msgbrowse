// Package shellnotes is the desktop shell's diagnostics ring buffer (issue
// #167): a small bounded log of systray/dock/window-chrome events the shell
// records at startup, surfaced on the web app's Logs page via
// web.(*Server).SetShellNotes so failures are observable in-app instead of
// silently lost to a stderr no .app bundle user ever sees. Every note is also
// mirrored to slog, so terminal launches still show them live.
//
// Notes are shell-composed strings only — never message content, never
// request-derived input — and never persisted to disk, matching the Logs
// page's existing posture (SPEC-0013 "Error Handling Standards").
//
// The package is pure Go (no Wails import, no cgo, no build tag) so it is
// unit-testable headlessly with CGO_ENABLED=0.
package shellnotes

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/joestump/msgbrowse/internal/web"
)

// maxNotes bounds the buffer. Shell diagnostics are a handful of startup
// lines plus occasional dock-policy transitions; 64 is far more than a
// session produces while still hard-capping memory if something misbehaves
// and logs in a loop.
const maxNotes = 64

// Log is a bounded, concurrency-safe ring buffer of shell diagnostics.
// The zero value is not usable; construct with New.
type Log struct {
	mu     sync.Mutex
	notes  []web.ShellNote
	logger *slog.Logger
	now    func() time.Time // injectable clock for tests
}

// New returns an empty Log mirroring every note to logger (nil means
// slog.Default()).
func New(logger *slog.Logger) *Log {
	if logger == nil {
		logger = slog.Default()
	}
	return &Log{logger: logger, now: time.Now}
}

// Infof records an informational note (formatted like fmt.Sprintf).
func (l *Log) Infof(format string, args ...any) {
	l.add(web.ShellNoteInfo, fmt.Sprintf(format, args...))
}

// Errorf records an error note (formatted like fmt.Sprintf).
func (l *Log) Errorf(format string, args ...any) {
	l.add(web.ShellNoteError, fmt.Sprintf(format, args...))
}

func (l *Log) add(level, msg string) {
	l.mu.Lock()
	l.notes = append(l.notes, web.ShellNote{Time: l.now(), Level: level, Message: msg})
	if len(l.notes) > maxNotes {
		// Drop the oldest; the newest diagnostics are the actionable ones.
		l.notes = l.notes[len(l.notes)-maxNotes:]
	}
	l.mu.Unlock()

	if level == web.ShellNoteError {
		l.logger.Error(msg)
	} else {
		l.logger.Info(msg)
	}
}

// Snapshot returns a copy of the notes in record order. It is the provider
// web.(*Server).SetShellNotes calls per /logs render, so it must be (and is)
// safe under concurrent Infof/Errorf.
func (l *Log) Snapshot() []web.ShellNote {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]web.ShellNote, len(l.notes))
	copy(out, l.notes)
	return out
}
