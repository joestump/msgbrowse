package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joestump/msgbrowse/internal/config"
	"github.com/joestump/msgbrowse/internal/signal"
	"github.com/joestump/msgbrowse/internal/source"
	"github.com/joestump/msgbrowse/internal/store"
)

// newJournalServer wires a Server over an EMPTY store (no fixture archive) so
// journal-day counts are exactly what the test seeds — deterministic pagination.
func newJournalServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "journal-web.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{DataDir: t.TempDir()}
	srv, err := NewServer(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv, st
}

// seedJournalDays inserts one message per date into a conversation and builds
// the mechanical journal, so journal_days holds exactly len(dates) rows.
func seedJournalDays(t *testing.T, st *store.Store, conv string, dates []string) {
	t.Helper()
	ctx := context.Background()
	id, err := st.UpsertConversation(ctx, source.Signal, conv)
	if err != nil {
		t.Fatal(err)
	}
	msgs := make([]signal.Message, 0, len(dates))
	for _, d := range dates {
		ts := d + " 09:00:00"
		parsed, _ := time.Parse(signal.TimestampLayout, ts)
		msgs = append(msgs, signal.Message{
			Conversation: conv, Timestamp: parsed, TimestampRaw: ts, Sender: conv, Body: "note on " + d,
		})
	}
	if _, err := st.ReplaceConversationMessages(ctx, id, source.Signal, msgs); err != nil {
		t.Fatal(err)
	}
	days, err := st.BuildJournalDays(ctx, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range days {
		if err := st.PutJournalDay(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJournalEmptyState(t *testing.T) {
	srv, _ := newJournalServer(t)
	rec := get(t, srv, "/journal")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No journal entries yet") {
		t.Error("empty state message missing")
	}
	if !strings.Contains(body, "msgbrowse journal") {
		t.Error("empty state should point at the `msgbrowse journal` command")
	}
}

func TestJournalRendersDaysAndDigest(t *testing.T) {
	srv, st := newJournalServer(t)
	seedJournalDays(t, st, "Harper", []string{"2023-05-01", "2023-05-02"})
	if err := st.PutDayDigest(context.Background(), store.JournalDigest{
		Day: "2023-05-02", Model: "m", PromptVersion: "pv", Body: "A bright day with Harper.",
	}); err != nil {
		t.Fatal(err)
	}

	rec := get(t, srv, "/journal")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<main id="main-content"`) {
		t.Error("full page missing #main-content")
	}
	if !strings.Contains(body, "May 2, 2023") || !strings.Contains(body, "A bright day with Harper.") {
		t.Error("digested day's label or digest body missing")
	}
	// The undigested day falls back to its mechanical who-summary.
	if !strings.Contains(body, "May 1, 2023") || !strings.Contains(body, "Most with Harper") {
		t.Error("undigested day should show the mechanical summary")
	}
}

func TestJournalPartialOmitsShell(t *testing.T) {
	srv, st := newJournalServer(t)
	seedJournalDays(t, st, "Harper", []string{"2023-05-01"})

	full := get(t, srv, "/journal").Body.String()
	if !strings.Contains(full, "<!doctype html>") {
		t.Error("full-page render should carry the document shell")
	}

	req := httptest.NewRequest(http.MethodGet, "/journal", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	partial := rec.Body.String()
	if strings.Contains(partial, "<!doctype html>") || strings.Contains(partial, "<html") {
		t.Error("boosted partial must not carry the document shell")
	}
	if !strings.Contains(partial, `<main id="main-content"`) {
		t.Error("boosted partial should still carry the #main-content region")
	}
}

func TestJournalDigestTextIsEscaped(t *testing.T) {
	srv, st := newJournalServer(t)
	seedJournalDays(t, st, "Harper", []string{"2023-05-01"})
	// Untrusted model output containing markup must be escaped, never live.
	if err := st.PutDayDigest(context.Background(), store.JournalDigest{
		Day: "2023-05-01", Model: "m", PromptVersion: "pv", Body: "<script>alert(1)</script> sneaky",
	}); err != nil {
		t.Fatal(err)
	}
	body := get(t, srv, "/journal").Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("digest markup was rendered live — html/template escaping bypassed")
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("digest markup should appear HTML-escaped")
	}
}

func TestJournalPaginationFirstPageAndContinuation(t *testing.T) {
	srv, st := newJournalServer(t)
	dates := make([]string, 31)
	for i := range dates {
		dates[i] = fmt.Sprintf("2023-01-%02d", i+1) // 2023-01-01 .. 2023-01-31
	}
	seedJournalDays(t, st, "Harper", dates)

	body := get(t, srv, "/journal").Body.String()
	if got := strings.Count(body, "journal-day-head"); got != journalPageSize {
		t.Errorf("first page day cards = %d, want %d", got, journalPageSize)
	}
	// Newest-first: the first page ends at 2023-01-02, so the cursor is that day.
	if !strings.Contains(body, "/journal/items?after_day=2023-01-02") {
		t.Errorf("expected a continuation link cursored at 2023-01-02")
	}

	req := httptest.NewRequest(http.MethodGet, "/journal/items?after_day=2023-01-02", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	cont := rec.Body.String()
	if got := strings.Count(cont, "journal-day-head"); got != 1 {
		t.Errorf("continuation day cards = %d, want 1 (the leftover oldest day)", got)
	}
	if !strings.Contains(cont, "January 1, 2023") {
		t.Error("continuation should carry the oldest day")
	}
	if strings.Contains(cont, "after_day=") {
		t.Error("last page must not advertise a further continuation")
	}
}
