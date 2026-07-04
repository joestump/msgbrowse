// Headless tests for the shell diagnostics ring buffer (issue #167):
// ordering, levels, the bound, snapshot isolation, and slog mirroring.
package shellnotes

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joestump/msgbrowse/internal/web"
)

func TestRecordsLevelsAndOrder(t *testing.T) {
	l := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	base := time.Date(2026, 7, 4, 9, 0, 0, 0, time.UTC)
	i := 0
	l.now = func() time.Time { i++; return base.Add(time.Duration(i) * time.Second) }

	l.Infof("menubar: registering status item (%s)", "darwin")
	l.Errorf("menubar: status item did not register within %ds", 30)

	notes := l.Snapshot()
	if len(notes) != 2 {
		t.Fatalf("Snapshot len = %d, want 2", len(notes))
	}
	if notes[0].Level != web.ShellNoteInfo || notes[0].Message != "menubar: registering status item (darwin)" {
		t.Errorf("first note = %+v; want the formatted info note", notes[0])
	}
	if notes[1].Level != web.ShellNoteError || notes[1].Message != "menubar: status item did not register within 30s" {
		t.Errorf("second note = %+v; want the formatted error note", notes[1])
	}
	if !notes[1].Time.After(notes[0].Time) {
		t.Error("notes should carry increasing record times")
	}
}

func TestBufferIsBounded(t *testing.T) {
	l := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	for n := 0; n < maxNotes+10; n++ {
		l.Infof("note %d", n)
	}
	notes := l.Snapshot()
	if len(notes) != maxNotes {
		t.Fatalf("Snapshot len = %d, want the %d cap", len(notes), maxNotes)
	}
	// The oldest were dropped; the newest survive.
	if notes[len(notes)-1].Message != fmt.Sprintf("note %d", maxNotes+9) {
		t.Errorf("newest note = %q; the ring must keep the most recent entries", notes[len(notes)-1].Message)
	}
	if notes[0].Message != "note 10" {
		t.Errorf("oldest kept note = %q, want %q", notes[0].Message, "note 10")
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	l := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	l.Infof("original")
	snap := l.Snapshot()
	snap[0].Message = "mutated"
	if l.Snapshot()[0].Message != "original" {
		t.Error("Snapshot must return a copy the caller cannot mutate")
	}
}

func TestMirrorsToSlog(t *testing.T) {
	var buf bytes.Buffer
	l := New(slog.New(slog.NewTextHandler(&buf, nil)))
	l.Infof("tray up")
	l.Errorf("tray down")
	out := buf.String()
	if !strings.Contains(out, "tray up") || !strings.Contains(out, "level=INFO") {
		t.Errorf("slog output missing the info mirror: %q", out)
	}
	if !strings.Contains(out, "tray down") || !strings.Contains(out, "level=ERROR") {
		t.Errorf("slog output missing the error mirror: %q", out)
	}
}

// TestConcurrentUse exercises the provider contract: Snapshot (the /logs
// render path) racing recorders must be safe. Run with -race in CI.
func TestConcurrentUse(t *testing.T) {
	l := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				l.Infof("worker %d note %d", w, n)
				_ = l.Snapshot()
			}
		}(w)
	}
	wg.Wait()
	if len(l.Snapshot()) != maxNotes {
		t.Errorf("after 200 notes the buffer should sit at its %d cap", maxNotes)
	}
}
