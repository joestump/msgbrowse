package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/joestump/msgbrowse/internal/embedsvc"
	"github.com/joestump/msgbrowse/internal/llm"
)

// fakeEmbedIndexer is a test double for the EmbedIndexer seam. It records
// every Kick so the security tests can assert a rejected POST started NO run,
// and lets a test script the status a render reads.
type fakeEmbedIndexer struct {
	status embedsvc.Status
	notes  []embedsvc.Note
	kicks  atomic.Int32
}

func (f *fakeEmbedIndexer) Status() embedsvc.Status { return f.status }
func (f *fakeEmbedIndexer) Notes() []embedsvc.Note  { return f.notes }
func (f *fakeEmbedIndexer) Kick() bool {
	f.kicks.Add(1)
	return true
}

// embedConfigured wires a live LLM configurator whose embed model is set, so
// the card leaves the "unconfigured" state.
func embedConfigured(srv *Server) {
	srv.SetLLMConfig(&fakeLLMConfigurator{cur: llm.Settings{
		BaseURL: "http://llm.test:4000/v1", EmbedModel: "test-embed", ChatModel: "test-chat",
	}})
}

// TestEmbedCardUnconfigured: with no embed model set anywhere, the Providers
// page renders the quiet off-state line with a link to the Settings → LLM tab
// — and no progress bar, no Resume, no polling.
func TestEmbedCardUnconfigured(t *testing.T) {
	srv := newEmptyStoreServer(t) // zero config: llm.embed_model is empty
	body := get(t, srv, "/providers").Body.String()

	if !contains(body, "Semantic search is off") {
		t.Error("unconfigured card missing the off-state line")
	}
	if !contains(body, `href="/settings/llm"`) {
		t.Error("unconfigured card missing the boosted link to Settings → LLM")
	}
	for _, forbidden := range []string{"<progress", "Resume indexing", `hx-get="/setup/embed/status"`} {
		if contains(body, forbidden) {
			t.Errorf("unconfigured card must not render %q", forbidden)
		}
	}
}

// TestEmbedCardRunning: while the job runs, the card carries a native
// <progress value max> bar, the num-formatted "Indexing N of M messages"
// line, and the 2s self-poll of /setup/embed/status.
func TestEmbedCardRunning(t *testing.T) {
	srv := newEmptyStoreServer(t)
	embedConfigured(srv)
	srv.SetEmbedIndexer(&fakeEmbedIndexer{status: embedsvc.Status{
		State: embedsvc.StateRunning, Model: "test-embed", Processed: 1500, Total: 12000,
	}})

	body := get(t, srv, "/providers").Body.String()
	for _, want := range []string{
		`<progress class="embed-progress-bar" value="1500" max="12000">`,
		"Indexing 1,500 of 12,000 messages", // num-formatted (#178)
		`hx-get="/setup/embed/status"`,
		`hx-trigger="every 2s"`,
	} {
		if !contains(body, want) {
			t.Errorf("running card missing %q", want)
		}
	}
	if contains(body, "Resume indexing") {
		t.Error("running card must not offer Resume")
	}
}

// TestEmbedCardRunningIndeterminate: before the run has counted its work
// (Total 0), the bar renders indeterminate — no value="0" max="0" garbage.
func TestEmbedCardRunningIndeterminate(t *testing.T) {
	srv := newEmptyStoreServer(t)
	embedConfigured(srv)
	srv.SetEmbedIndexer(&fakeEmbedIndexer{status: embedsvc.Status{
		State: embedsvc.StateRunning, Model: "test-embed",
	}})

	body := get(t, srv, "/providers").Body.String()
	if !contains(body, "<progress") || contains(body, `max="0"`) {
		t.Errorf("counting card should render an indeterminate bar; got %q", body)
	}
	if !contains(body, "Preparing the semantic index") {
		t.Error("counting card missing its status line")
	}
}

// TestEmbedCardBehind: idle with unembedded messages — counts (num-formatted)
// plus the explicit Resume affordance, and NO polling attributes.
func TestEmbedCardBehind(t *testing.T) {
	srv, _, _ := newTestServer(t) // fixture store: real unembedded messages
	embedConfigured(srv)
	srv.SetEmbedIndexer(&fakeEmbedIndexer{}) // idle, never ran

	body := get(t, srv, "/providers").Body.String()
	if !contains(body, "await semantic indexing") {
		t.Error("behind card missing the counts line")
	}
	if !contains(body, `hx-post="/setup/embed/resume"`) || !contains(body, "Resume indexing") {
		t.Error("behind card missing the Resume affordance")
	}
	if contains(body, `hx-trigger="every 2s"`) {
		t.Error("idle card must not poll")
	}
	if !contains(body, "X-Setup-Token") {
		t.Error("Resume control missing the per-session token header")
	}
}

// TestEmbedCardCurrent: idle with every embeddable message indexed — the
// "index current" line, no Resume, no polling. This is also the poll-stop
// proof: the fragment route returns this non-polling render once a run ends.
func TestEmbedCardCurrent(t *testing.T) {
	srv, st, _ := newTestServer(t)
	embedConfigured(srv)
	srv.SetEmbedIndexer(&fakeEmbedIndexer{status: embedsvc.Status{State: embedsvc.StateDone}})

	// Index everything the fixture holds (synthetic bodies only).
	ctx := context.Background()
	targets, err := st.MessagesNeedingEmbedding(ctx, "test-embed", 10000)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		if err := st.PutEmbedding(ctx, tg.Hash, "test-embed", []float32{1, 0}); err != nil {
			t.Fatal(err)
		}
	}

	body := get(t, srv, "/providers").Body.String()
	if !contains(body, "Semantic index current") {
		t.Error("current card missing the up-to-date line")
	}
	for _, forbidden := range []string{"Resume indexing", `hx-trigger="every 2s"`, "<progress"} {
		if contains(body, forbidden) {
			t.Errorf("current card must not render %q", forbidden)
		}
	}

	// The standalone fragment (what a final poll receives) is the same
	// non-polling render — polling stops with the job.
	frag := get(t, srv, "/setup/embed/status").Body.String()
	if !contains(frag, "Semantic index current") || contains(frag, `hx-trigger="every 2s"`) {
		t.Errorf("post-run fragment should be the non-polling current card; got %q", frag)
	}
}

// TestEmbedCardFailed: the failed state is an honest one-liner pointing at
// Settings → Logs, with the Resume affordance as the retry.
func TestEmbedCardFailed(t *testing.T) {
	srv, _, _ := newTestServer(t)
	embedConfigured(srv)
	srv.SetEmbedIndexer(&fakeEmbedIndexer{status: embedsvc.Status{
		State: embedsvc.StateFailed, Err: errors.New("connection refused"),
	}})

	body := get(t, srv, "/providers").Body.String()
	if !contains(body, "Semantic indexing failed") {
		t.Error("failed card missing the failure line")
	}
	if !contains(body, `href="/logs"`) {
		t.Error("failed card missing the Settings → Logs link")
	}
	if !contains(body, "Resume indexing") {
		t.Error("failed card missing the retry affordance")
	}
	if contains(body, `hx-trigger="every 2s"`) {
		t.Error("failed card must not poll")
	}
	// The raw provider error stays in Logs, not on the Providers card.
	if contains(body, "connection refused") {
		t.Error("failed card should not inline the raw error text")
	}
}

// embedResumePOST posts /setup/embed/resume with the given origin + token.
func embedResumePOST(t *testing.T, srv *Server, origin, token string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	if token != "" {
		form.Set(setupTokenField, token)
	}
	req := httptest.NewRequest(http.MethodPost, "/setup/embed/resume", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestEmbedResumeCrossOriginRejected: the Resume POST carries the identical
// privileged gate as the other Setup POSTs — a cross-origin POST is 403 and
// starts NO run, even with a valid token.
func TestEmbedResumeCrossOriginRejected(t *testing.T) {
	srv := newEmptyStoreServer(t)
	embedConfigured(srv)
	fe := &fakeEmbedIndexer{}
	srv.SetEmbedIndexer(fe)

	tok := mintToken(t, srv)
	rec := embedResumePOST(t, srv, "http://evil.example", tok)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin resume status = %d, want 403", rec.Code)
	}
	if fe.kicks.Load() != 0 {
		t.Fatalf("cross-origin resume started %d runs, want 0", fe.kicks.Load())
	}
}

// TestEmbedResumeMissingTokenRejected: a same-origin POST with no token is 403
// and starts no run.
func TestEmbedResumeMissingTokenRejected(t *testing.T) {
	srv := newEmptyStoreServer(t)
	embedConfigured(srv)
	fe := &fakeEmbedIndexer{}
	srv.SetEmbedIndexer(fe)

	rec := embedResumePOST(t, srv, selfOrigin, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing-token resume status = %d, want 403", rec.Code)
	}
	if fe.kicks.Load() != 0 {
		t.Fatalf("missing-token resume started %d runs, want 0", fe.kicks.Load())
	}
}

// TestEmbedResumeInvalidTokenRejected: a well-formed but never-minted token is
// 403 and starts no run.
func TestEmbedResumeInvalidTokenRejected(t *testing.T) {
	srv := newEmptyStoreServer(t)
	embedConfigured(srv)
	fe := &fakeEmbedIndexer{}
	srv.SetEmbedIndexer(fe)

	rec := embedResumePOST(t, srv, selfOrigin, strings.Repeat("ab", 32))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("invalid-token resume status = %d, want 403", rec.Code)
	}
	if fe.kicks.Load() != 0 {
		t.Fatalf("invalid-token resume started %d runs, want 0", fe.kicks.Load())
	}
}

// TestEmbedResumeHappyPath: a valid same-origin+token POST kicks the job
// exactly once and answers with the refreshed card fragment.
func TestEmbedResumeHappyPath(t *testing.T) {
	srv := newEmptyStoreServer(t)
	embedConfigured(srv)
	fe := &fakeEmbedIndexer{}
	srv.SetEmbedIndexer(fe)

	tok := mintToken(t, srv)
	rec := embedResumePOST(t, srv, selfOrigin, tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume status = %d, want 200", rec.Code)
	}
	if fe.kicks.Load() != 1 {
		t.Fatalf("resume kicked %d times, want 1", fe.kicks.Load())
	}
	if !contains(rec.Body.String(), `id="embed-progress"`) {
		t.Errorf("resume response is not the card fragment: %q", rec.Body.String())
	}
}

// TestLLMSaveKicksEmbedJob: a successful Settings → LLM save triggers the
// embed job (the save-when-behind trigger); a validation-failing save does
// not (nothing was applied, nothing starts).
func TestLLMSaveKicksEmbedJob(t *testing.T) {
	srv := newEmptyStoreServer(t)
	fc := &fakeLLMConfigurator{}
	srv.SetLLMConfig(fc)
	fe := &fakeEmbedIndexer{}
	srv.SetEmbedIndexer(fe)

	// Validation failure first: no apply, no kick.
	tok := mintToken(t, srv)
	rec := llmPOST(t, srv, selfOrigin, tok, map[string]string{"base_url": "not a url"})
	if rec.Code != http.StatusOK {
		t.Fatalf("invalid save status = %d, want 200 re-render", rec.Code)
	}
	if len(fc.applied) != 0 || fe.kicks.Load() != 0 {
		t.Fatalf("invalid save applied %d / kicked %d, want 0/0", len(fc.applied), fe.kicks.Load())
	}

	// Valid save: applied once, kicked once.
	tok = mintToken(t, srv)
	rec = llmPOST(t, srv, selfOrigin, tok, validLLMForm())
	if rec.Code != http.StatusOK {
		t.Fatalf("valid save status = %d, want 200", rec.Code)
	}
	if len(fc.applied) != 1 {
		t.Fatalf("valid save applied %d settings, want 1", len(fc.applied))
	}
	if fe.kicks.Load() != 1 {
		t.Fatalf("valid save kicked the embed job %d times, want 1", fe.kicks.Load())
	}
}

// TestLogsShowsEmbedStream: the Logs page renders the embedding job's own
// labelled stream when it has run history.
func TestLogsShowsEmbedStream(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.SetEmbedIndexer(&fakeEmbedIndexer{notes: []embedsvc.Note{
		{Level: embedsvc.NoteInfo, Message: "embedding run started (model test-embed)"},
		{Level: embedsvc.NoteInfo, Message: "embedded 42 messages in 3 batches (1.2s)"},
	}})

	body := get(t, srv, "/logs").Body.String()
	if !contains(body, "Embedding") || !contains(body, "embedded 42 messages in 3 batches") {
		t.Error("Logs page missing the embedding stream")
	}
}
