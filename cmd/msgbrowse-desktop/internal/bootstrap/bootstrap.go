// Package bootstrap serves the one-shot trampoline page the webview loads
// from the Wails asset scheme before anything else. Its only job is to point
// the window at the embedded server's loopback URL: every real request flows
// over loopback HTTP to internal/web, never through the Wails asset handler
// (SPEC-0010 design decision "Loopback HTTP on an ephemeral port, not the
// Wails asset handler"). Navigation happens via <meta http-equiv="refresh">
// — the page carries no scripts.
//
// The page renders as a slate splash (issue #166): before this, launch
// flashed a white page with a bare "Open msgbrowse" link for the instant the
// trampoline was visible. Now it paints the slate app background (#0f1216,
// the SPEC-0006 base-100 token) with a centered wordmark and a subtle
// "Loading…" line, so launch reads as the app coming up, not a broken page.
// The splash is deliberately dark in both themes — it shows for well under a
// second and matches the window's native background colour (set in main.go),
// so there is no flash either side of it.
//
// CSP: the trampoline is served by the shell's own asset handler, so it sets
// its own policy — the app's strict middleware never touches it. Because the
// splash needs styling and this page is a single server-owned constant with
// zero user content, the styles live in one <style> element whitelisted by
// its exact sha256 hash: `style-src 'sha256-…'`. That keeps the no-
// 'unsafe-inline' posture (ADR-0010) — only this precise style block can
// apply, scripts remain forbidden entirely (default-src 'none').
//
// The package is pure Go (no Wails import, no cgo, no build tag) so the
// splash contract is unit-testable on headless machines with CGO_ENABLED=0.
package bootstrap

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
)

// splashCSS is the trampoline's entire stylesheet: the slate splash (#166).
// Colors are the SPEC-0006 slate tokens (base-100 background, base-content
// text, dimmed secondary) hard-coded because this page renders before app.css
// (and everything else) is reachable. The CSP hash below is computed from
// exactly these bytes — the <style> element's content must be this constant,
// byte for byte.
const splashCSS = `html,body{height:100%;margin:0}
body{display:flex;align-items:center;justify-content:center;background:#0f1216;color:#dbe2ea;font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;-webkit-user-select:none;user-select:none}
main{text-align:center}
h1{margin:0 0 .35rem;font-size:1.25rem;font-weight:700;letter-spacing:-.02em}
p{margin:0;font-size:.8125rem;color:#7c8694}
a{color:#7c8694}`

// styleHash is the CSP source expression whitelisting exactly splashCSS as an
// inline <style> element: 'sha256-<base64(sha256(splashCSS))>'.
var styleHash = func() string {
	sum := sha256.Sum256([]byte(splashCSS))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

// Handler returns the trampoline handler for the Wails asset server.
//
// url is the embedded server's base URL derived from the bound listener
// (http://127.0.0.1:<port>), never user input. The plain link inside the
// splash is the no-refresh fallback: if the meta refresh somehow fails the
// user still has a way in, styled as part of the splash instead of a naked
// white page.
func Handler(url string) http.Handler {
	page := fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="0;url=%[1]s">
<title>msgbrowse</title>
<style>%[2]s</style>
</head>
<body>
<main>
<h1>msgbrowse</h1>
<p>Loading… <a href="%[1]s">Open msgbrowse</a></p>
</main>
</body>
</html>
`, url, splashCSS)
	csp := "default-src 'none'; style-src " + styleHash
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		_, _ = w.Write([]byte(page))
	})
}
