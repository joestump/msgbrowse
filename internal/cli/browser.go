package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"runtime"
	"time"
)

// reachableHostPort normalizes a listen address into a host:port a client can
// actually connect to: a wildcard/empty bind host (0.0.0.0, ::, "") becomes
// loopback. If listenAddr isn't host:port, it is returned unchanged. Shared by
// browserURL (the opened URL) and openWhenReady (the readiness probe) so the two
// can never disagree about where the server is.
func reachableHostPort(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return listenAddr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// browserURL returns the http URL a browser should open for a listen address
// (a browser cannot open http://0.0.0.0). Used for the serve --open convenience.
func browserURL(listenAddr string) string {
	return "http://" + reachableHostPort(listenAddr)
}

// browserOpenCommand returns the OS command + args to open url in the default
// browser, and whether the OS is supported. Split out so it is unit-testable
// without launching anything.
func browserOpenCommand(goos, url string) (name string, args []string, ok bool) {
	switch goos {
	case "darwin":
		return "open", []string{url}, true
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}, true
	case "linux":
		return "xdg-open", []string{url}, true
	default:
		return "", nil, false
	}
}

// openBrowser launches the default browser at url. Best-effort.
func openBrowser(url string) error {
	name, args, ok := browserOpenCommand(runtime.GOOS, url)
	if !ok {
		return fmt.Errorf("opening a browser is not supported on %s", runtime.GOOS)
	}
	return exec.Command(name, args...).Start()
}

// openWhenReady waits until listenAddr accepts a TCP connection (a few seconds
// max), then opens the browser once. It is best-effort: on a headless host with
// no opener it just logs at debug and returns, never affecting the server.
func openWhenReady(ctx context.Context, listenAddr string, log *slog.Logger) {
	dialAddr := reachableHostPort(listenAddr)
	d := net.Dialer{Timeout: 200 * time.Millisecond}
	for i := 0; i < 50; i++ { // ~5s budget
		if ctx.Err() != nil {
			return
		}
		conn, err := d.DialContext(ctx, "tcp", dialAddr)
		if err == nil {
			_ = conn.Close()
			url := browserURL(listenAddr)
			if err := openBrowser(url); err != nil {
				log.Debug("could not open browser", "url", url, "error", err)
			} else {
				log.Info("opened browser", "url", url)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Debug("server not ready in time; skipping browser open", "addr", dialAddr)
}
