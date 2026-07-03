package web

import (
	"compress/gzip"
	"net/http"
	"strings"
)

// gzipMinSize is the smallest body worth compressing. Below this the gzip
// header/dictionary overhead eats the savings, so tiny responses (error
// strings, empty search results) go out as-is.
const gzipMinSize = 1024

// compressibleTypes are the response content types the gzip middleware
// compresses (SPEC-0008 REQ-0008-007). Everything else — notably media, which
// is already compressed — passes through untouched.
var compressibleTypes = map[string]bool{
	"text/html":              true,
	"text/css":               true,
	"application/javascript": true,
	"application/json":       true,
	"image/svg+xml":          true,
}

// gzipMiddleware compresses eligible responses with stdlib gzip when the
// client advertises Accept-Encoding: gzip (REQ-0008-007). /media/ responses
// are exempt wholesale: attachments are served raw (images/video are already
// compressed, and http.ServeContent range semantics must stay intact).
//
// The decision is deferred until enough of the body has been written: only
// responses with a compressible Content-Type and at least gzipMinSize bytes
// get Content-Encoding: gzip (with Content-Length dropped, since the
// compressed size is unknown up front). Vary: Accept-Encoding is set on every
// response the middleware might compress so caches never mix encodings.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/media/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.finish()
		next.ServeHTTP(gw, r)
	})
}

// acceptsGzip reports whether the client advertises gzip support. The header
// is treated as an opaque token list; q=0 refusals are rare enough locally
// that a substring check on the token is sufficient (and never unsafe — a
// client sending "gzip;q=0" merely receives what it asked to disable).
func acceptsGzip(r *http.Request) bool {
	for _, enc := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		token, _, _ := strings.Cut(strings.TrimSpace(enc), ";")
		if token == "gzip" {
			return true
		}
	}
	return false
}

// gzipResponseWriter defers the compress-or-not decision: writes buffer until
// gzipMinSize is reached (then the response commits to gzip if eligible) or
// the handler returns (then the small body flushes uncompressed). The status
// code is held back until the decision because Content-Encoding must be set
// before WriteHeader reaches the real writer.
type gzipResponseWriter struct {
	http.ResponseWriter
	status      int  // deferred status; 0 means never explicitly set
	decided     bool // compress-or-not decision made and headers flushed
	compressing bool
	buf         []byte
	zw          *gzip.Writer
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.decided {
		return // headers already flushed; late WriteHeader is a handler bug net/http also ignores
	}
	if g.status == 0 {
		g.status = code
	}
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.decided {
		g.buf = append(g.buf, p...)
		if len(g.buf) < gzipMinSize {
			return len(p), nil
		}
		if err := g.decide(); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	if g.compressing {
		return g.zw.Write(p)
	}
	return g.ResponseWriter.Write(p)
}

// decide commits the response: gzip when eligible, passthrough otherwise, and
// flushes the status line plus everything buffered so far.
func (g *gzipResponseWriter) decide() error {
	g.decided = true
	if g.eligible() {
		h := g.Header()
		h.Del("Content-Length") // compressed size is unknown
		h.Set("Content-Encoding", "gzip")
		g.compressing = true
		g.writeStatus()
		g.zw = gzip.NewWriter(g.ResponseWriter)
		_, err := g.zw.Write(g.buf)
		g.buf = nil
		return err
	}
	g.writeStatus()
	if len(g.buf) > 0 {
		_, err := g.ResponseWriter.Write(g.buf)
		g.buf = nil
		return err
	}
	return nil
}

// eligible reports whether the committed response may be gzipped: a
// compressible Content-Type, no prior Content-Encoding, no partial/bodyless
// status (206 ranges describe uncompressed offsets; 204/304 have no body).
func (g *gzipResponseWriter) eligible() bool {
	switch g.status {
	case http.StatusNoContent, http.StatusPartialContent, http.StatusNotModified:
		return false
	}
	h := g.Header()
	if h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" {
		return false
	}
	ct := h.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return compressibleTypes[strings.TrimSpace(ct)]
}

func (g *gzipResponseWriter) writeStatus() {
	if g.status == 0 {
		g.status = http.StatusOK
	}
	g.ResponseWriter.WriteHeader(g.status)
}

// finish completes the response after the handler returns: bodies that never
// reached gzipMinSize flush uncompressed (Content-Length, if any, is still
// correct because the bytes are unmodified); compressed bodies close the gzip
// stream so the trailer is written.
func (g *gzipResponseWriter) finish() {
	if !g.decided {
		g.decided = true
		g.writeStatus()
		if len(g.buf) > 0 {
			_, _ = g.ResponseWriter.Write(g.buf)
			g.buf = nil
		}
		return
	}
	if g.zw != nil {
		_ = g.zw.Close()
	}
}

// Flush satisfies http.Flusher for handlers that stream. An undecided response
// commits first (a streaming handler cannot wait for the size threshold), then
// pending compressed bytes and the underlying writer flush.
func (g *gzipResponseWriter) Flush() {
	if !g.decided {
		_ = g.decide()
	}
	if g.zw != nil {
		_ = g.zw.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
