// Headless tests for the quick menu's pure plumbing, run with CGO_ENABLED=0
// by `make desktop-test`: status-line formatting, copy payload routing, the
// acknowledgment retitle-and-revert cycle, and health refresh.
package tray

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestStatusTitle pins the SPEC-0010 status line: endpoint (with its live
// port) plus running/degraded state.
func TestStatusTitle(t *testing.T) {
	const endpoint = "http://127.0.0.1:49152/mcp"
	up := StatusTitle(endpoint, true)
	if !strings.Contains(up, endpoint) || !strings.Contains(up, "running") {
		t.Errorf("healthy status = %q; want endpoint and \"running\"", up)
	}
	down := StatusTitle(endpoint, false)
	if !strings.Contains(down, endpoint) || !strings.Contains(down, "degraded") {
		t.Errorf("unhealthy status = %q; want endpoint and \"degraded\"", down)
	}
	if !strings.Contains(up, ":49152") {
		t.Errorf("status = %q; want the port visible (SPEC-0010 running + port)", up)
	}
}

// fakeItem records every title set on it, in order. It is mutex-guarded
// because the menu loop writes titles from its own goroutine while tests
// read them (after synchronizing order through the event channels).
type fakeItem struct {
	mu     sync.Mutex
	titles []string
}

func (f *fakeItem) SetTitle(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.titles = append(f.titles, s)
}

func (f *fakeItem) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.titles) == 0 {
		return ""
	}
	return f.titles[len(f.titles)-1]
}

// harness runs a Menu.Loop against channel-driven events with a manual
// timer, returning everything a test needs to script interactions.
type harness struct {
	view, pair, status, config, quit chan struct{}
	refresh                          chan time.Time
	ack                              chan time.Time
	statusItem, configItem           *fakeItem
	copied                           chan string
	shown, paired, quitCalled        chan struct{}
	healthy                          atomic.Bool // read by Probe from the loop goroutine
	done                             chan struct{}
	stopped                          chan struct{}
}

func newHarness(copyOK bool) *harness {
	h := &harness{
		view: make(chan struct{}), pair: make(chan struct{}),
		status: make(chan struct{}), config: make(chan struct{}),
		quit: make(chan struct{}), refresh: make(chan time.Time),
		ack:        make(chan time.Time),
		statusItem: &fakeItem{}, configItem: &fakeItem{},
		copied: make(chan string, 8), shown: make(chan struct{}, 8),
		paired: make(chan struct{}, 8), quitCalled: make(chan struct{}, 8),
		done: make(chan struct{}), stopped: make(chan struct{}),
	}
	h.healthy.Store(true)
	m := &Menu{
		Endpoint:   "http://127.0.0.1:40000/mcp",
		ConfigJSON: `{"mcpServers":{}}`,
		Actions: Actions{
			ShowWindow:  func() { h.shown <- struct{}{} },
			OpenPairing: func() { h.paired <- struct{}{} },
			CopyText: func(s string) bool {
				h.copied <- s
				return copyOK
			},
			Quit:  func() { h.quitCalled <- struct{}{} },
			Probe: func() bool { return h.healthy.Load() },
		},
		After: func(time.Duration) <-chan time.Time { return h.ack },
	}
	go func() {
		m.Loop(h.done, Events{
			ViewClicked: h.view, PairClicked: h.pair,
			StatusClicked: h.status, ConfigClicked: h.config,
			QuitClicked: h.quit, Refresh: h.refresh,
		}, h.statusItem, h.configItem)
		close(h.stopped)
	}()
	return h
}

func (h *harness) stop(t *testing.T) {
	t.Helper()
	close(h.done)
	select {
	case <-h.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("menu loop did not stop")
	}
}

func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

// TestClicksInvokeActions routes each menu item to its shell action.
func TestClicksInvokeActions(t *testing.T) {
	h := newHarness(true)
	defer h.stop(t)

	h.view <- struct{}{}
	waitFor(t, h.shown, "show window")

	h.pair <- struct{}{}
	waitFor(t, h.paired, "open pairing")

	h.quit <- struct{}{}
	waitFor(t, h.quitCalled, "quit")
}

// TestStatusClickCopiesEndpointAndAcknowledges is the SPEC-0010 scenario
// "Copy MCP endpoint from the tray": activation copies the endpoint URL and
// the item retitles to acknowledge, then reverts when the ack timer fires.
func TestStatusClickCopiesEndpointAndAcknowledges(t *testing.T) {
	h := newHarness(true)
	defer h.stop(t)

	h.status <- struct{}{}
	if got := waitFor(t, h.copied, "endpoint copy"); got != "http://127.0.0.1:40000/mcp" {
		t.Errorf("copied %q; want the MCP endpoint URL", got)
	}
	h.refresh <- time.Time{} // synchronize: ack title is set before the next select
	if got := h.statusItem.last(); got != StatusCopiedTitle() {
		t.Errorf("status title after copy = %q; want %q", got, StatusCopiedTitle())
	}

	h.ack <- time.Time{} // ack expires
	h.refresh <- time.Time{}
	if got := h.statusItem.last(); got != StatusTitle("http://127.0.0.1:40000/mcp", true) {
		t.Errorf("status title after ack = %q; want the live status line", got)
	}
}

// TestRefreshNeverClobbersAcknowledgment: a health tick landing during the
// ack window must not overwrite the "Copied" retitle.
func TestRefreshNeverClobbersAcknowledgment(t *testing.T) {
	h := newHarness(true)
	defer h.stop(t)

	h.status <- struct{}{}
	waitFor(t, h.copied, "endpoint copy")
	h.refresh <- time.Time{} // tick during the ack window
	h.refresh <- time.Time{} // second tick proves the first completed
	if got := h.statusItem.last(); got != StatusCopiedTitle() {
		t.Errorf("refresh during ack rewrote the title to %q; want %q kept", got, StatusCopiedTitle())
	}
}

// TestConfigClickCopiesJSONBlock routes the full client-config payload to
// the clipboard action and acknowledges on the config item.
func TestConfigClickCopiesJSONBlock(t *testing.T) {
	h := newHarness(true)
	defer h.stop(t)

	h.config <- struct{}{}
	if got := waitFor(t, h.copied, "config copy"); got != `{"mcpServers":{}}` {
		t.Errorf("copied %q; want the config JSON block", got)
	}
	h.refresh <- time.Time{}
	if got := h.configItem.last(); got != ConfigCopiedTitle() {
		t.Errorf("config title after copy = %q; want %q", got, ConfigCopiedTitle())
	}

	h.ack <- time.Time{}
	h.refresh <- time.Time{}
	if got := h.configItem.last(); got != CopyConfigTitle {
		t.Errorf("config title after ack = %q; want %q restored", got, CopyConfigTitle)
	}
}

// TestFailedCopyDoesNotAcknowledge: when the clipboard write fails (e.g. no
// runtime context yet), the item must not claim "Copied".
func TestFailedCopyDoesNotAcknowledge(t *testing.T) {
	h := newHarness(false)
	defer h.stop(t)

	h.status <- struct{}{}
	waitFor(t, h.copied, "attempted copy")
	h.refresh <- time.Time{}
	if got := h.statusItem.last(); got == StatusCopiedTitle() {
		t.Error("status item acknowledged a copy that failed")
	}
}

// TestRefreshTracksHealth: ticks re-probe and the title flips between
// running and degraded (SPEC-0010 "Status accuracy").
func TestRefreshTracksHealth(t *testing.T) {
	h := newHarness(true)
	defer h.stop(t)

	h.healthy.Store(false)
	h.refresh <- time.Time{}
	h.refresh <- time.Time{} // synchronize
	if got := h.statusItem.last(); !strings.Contains(got, "degraded") {
		t.Errorf("status after unhealthy probe = %q; want degraded", got)
	}

	h.healthy.Store(true)
	h.refresh <- time.Time{}
	h.refresh <- time.Time{}
	if got := h.statusItem.last(); !strings.Contains(got, "running") {
		t.Errorf("status after recovery = %q; want running", got)
	}
}
