// Same-origin + per-session-token protection for the privileged Setup POSTs
// (SPEC-0013 §Security "Same-origin protection for privileged POSTs"). The
// read-only /setup GET renders detection cards under the ADR-0010 loopback
// single-user trust and needs no token; but /setup/enable (and, built here so
// #135's /setup/refresh + /setup/recheck reuse it) spawns a bundled exporter
// that reads a personal database — a privileged local action that MUST NOT be
// triggerable cross-origin, even under loopback, because another local process
// or a malicious page loaded in a browser could otherwise drive the exporter.
//
// The gate requires BOTH, and rejects with 403 BEFORE any subprocess starts:
//
//  1. Same-origin: the request's Origin (or, absent it, Sec-Fetch-Site:
//     same-origin, or a same-origin Referer) must match the server's own
//     loopback origin — the address the client actually reached us on.
//  2. A per-session token: minted at /setup render, submitted with the POST
//     (form field or the X-Setup-Token header htmx sends), and verified against
//     the server's live token set with a constant-time comparison.
//
// Governing: SPEC-0013 §Security "Same-origin protection for privileged POSTs"
// ("rejected unless same-origin AND a per-session token … rejected with 403 and
// MUST NOT start any subprocess"), §Security "Request body size limits".
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"sync"
)

// setupTokenField is the form field / header name the minted per-session token
// is submitted under. The hidden input in the Setup template carries it; htmx
// also mirrors it into the X-Setup-Token request header via hx-headers.
const (
	setupTokenField  = "setup_token"
	setupTokenHeader = "X-Setup-Token"
)

// setupBodyLimit caps the Setup POST bodies. They carry only a source enum plus
// the token — a few dozen bytes — so a kilobyte cap (SPEC-0013 §Security
// "Request body size limits": KB, not MB) rejects any malformed or oversized
// body before processing.
const setupBodyLimit = 4 << 10 // 4 KiB

// setupTokens is the server's live set of valid per-session Setup tokens. A
// token is minted each time /setup is rendered and remembered here until the
// process exits; the set is small (one per page view) and single-user, so it is
// never evicted — the loopback single-user model has no adversary hoarding
// tokens, and the process is short-lived per SPEC-0010's ephemeral-port design.
// Access is synchronized because handlers run concurrently.
type setupTokens struct {
	mu     sync.Mutex
	tokens map[string]struct{}
}

func newSetupTokens() *setupTokens {
	return &setupTokens{tokens: make(map[string]struct{})}
}

// mint generates a fresh 256-bit random token, records it as valid, and returns
// its hex string for embedding in the rendered Setup page.
func (s *setupTokens) mint() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b[:])
	s.mu.Lock()
	s.tokens[tok] = struct{}{}
	s.mu.Unlock()
	return tok, nil
}

// valid reports whether tok is a live minted token, using a constant-time
// compare against each candidate so a rejected token leaks no timing signal.
// (The set is tiny — one entry per page render — so the linear scan is cheap.)
func (s *setupTokens) valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for known := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(known), []byte(tok)) == 1 {
			return true
		}
	}
	return false
}

// checkSetupPOST enforces the same-origin + token gate on a privileged Setup
// POST. It returns true when the request may proceed; on failure it has already
// written a 403 and the caller MUST return without starting any work (SPEC-0013
// §Security "rejected with 403 and MUST NOT start any subprocess").
//
// It also installs the MaxBytesReader body cap so the subsequent form parse can
// never read an oversized body.
func (s *Server) checkSetupPOST(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, setupBodyLimit)

	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return false
	}

	// The token arrives either as the X-Setup-Token header (htmx hx-headers) or
	// the setup_token form field (a plain form POST fallback). Parse the form to
	// read the field; ParseForm respects the MaxBytesReader cap above.
	tok := r.Header.Get(setupTokenHeader)
	if tok == "" {
		// ParseForm reads the (capped) body; an oversized body errors here and is
		// treated as an invalid request.
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid request", http.StatusForbidden)
			return false
		}
		tok = r.PostFormValue(setupTokenField)
	}
	if !s.setupTokens.valid(tok) {
		http.Error(w, "missing or invalid setup token", http.StatusForbidden)
		return false
	}
	return true
}

// sameOrigin reports whether a state-changing request originates from this
// server's own loopback page. The check is layered to match browser behavior:
//
//   - Origin present: it must equal the request's own scheme://host (the address
//     the client reached us on). A cross-origin page sending a form/fetch POST
//     carries its OWN Origin, which will not match — that is exactly the attack
//     this rejects.
//   - No Origin (some same-origin GETs/navigations omit it) but
//     Sec-Fetch-Site present: accept only "same-origin"/"none"; reject
//     "cross-site"/"same-site". Modern browsers always send this on POSTs.
//   - Neither header (a bare programmatic client): fall back to Referer, which
//     must be same-origin. With no Origin, no Sec-Fetch-Site, and no Referer at
//     all, reject — a privileged POST with zero provenance is not trusted.
//
// The server's own origin is derived from the request (Host + the always-http
// loopback scheme, ADR-0010), so it is correct for both the desktop shell's
// ephemeral port and `msgbrowse serve`'s configured loopback bind with no
// mode-specific configuration.
func sameOrigin(r *http.Request) bool {
	self := "http://" + r.Host

	if origin := r.Header.Get("Origin"); origin != "" {
		return origin == self
	}

	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "cross-site", "same-site":
		return false
	}

	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		if err != nil {
			return false
		}
		return u.Scheme+"://"+u.Host == self
	}

	// No provenance headers at all: reject a privileged POST.
	return false
}
