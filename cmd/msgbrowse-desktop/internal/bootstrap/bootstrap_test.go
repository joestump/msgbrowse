// Headless tests for the trampoline splash (issue #166): the page must paint
// slate (no white flash), still meta-refresh to the embedded server, carry no
// scripts, and whitelist its single <style> element by exact CSP hash so the
// no-'unsafe-inline' posture holds. CGO_ENABLED=0, no webview needed.
package bootstrap

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

const testURL = "http://127.0.0.1:49152"

func serve(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler(testURL).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec
}

// TestTrampolineRedirectsToEmbeddedServer pins the original contract: a
// zero-delay meta refresh to the loopback URL plus a plain-link fallback.
func TestTrampolineRedirectsToEmbeddedServer(t *testing.T) {
	body := serve(t).Body.String()
	if !strings.Contains(body, `<meta http-equiv="refresh" content="0;url=`+testURL+`">`) {
		t.Error("trampoline missing the zero-delay meta refresh to the embedded server")
	}
	if !strings.Contains(body, `<a href="`+testURL+`">`) {
		t.Error("trampoline missing the no-refresh fallback link")
	}
}

// TestTrampolineIsSlateSplash verifies the #166 fix: the page styles itself as
// the slate splash (dark app background, wordmark, Loading line) instead of an
// unstyled white page.
func TestTrampolineIsSlateSplash(t *testing.T) {
	body := serve(t).Body.String()
	if !strings.Contains(body, "background:#0f1216") {
		t.Error("splash missing the slate base-100 background — launch would flash white")
	}
	if !strings.Contains(body, "<h1>msgbrowse</h1>") {
		t.Error("splash missing the centered wordmark")
	}
	if !strings.Contains(body, "Loading…") {
		t.Error("splash missing the Loading… line")
	}
}

// TestTrampolineCSPHashMatchesStyle recomputes the sha256 of the <style>
// element's exact content and requires the CSP to whitelist precisely that
// hash — no 'unsafe-inline', no scripts, nothing else (ADR-0010 posture kept
// on the one page outside the app middleware).
func TestTrampolineCSPHashMatchesStyle(t *testing.T) {
	rec := serve(t)
	body := rec.Body.String()
	csp := rec.Header().Get("Content-Security-Policy")

	m := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("trampoline has no <style> element")
	}
	sum := sha256.Sum256([]byte(m[1]))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"

	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q; want default-src 'none'", csp)
	}
	if !strings.Contains(csp, "style-src "+want) {
		t.Errorf("CSP = %q; want style-src %s matching the served <style> content", csp, want)
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP = %q; must not fall back to 'unsafe-inline'", csp)
	}
}

// TestTrampolineCarriesNoScripts keeps the page script-free: navigation is
// meta refresh only, so default-src 'none' (no script-src) stays honest.
func TestTrampolineCarriesNoScripts(t *testing.T) {
	rec := serve(t)
	if strings.Contains(rec.Body.String(), "<script") {
		t.Error("trampoline must not carry scripts")
	}
	for header, want := range map[string]string{
		"Content-Type":           "text/html; charset=utf-8",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q; want %q", header, got, want)
		}
	}
}
