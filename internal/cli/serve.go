package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/joestump/msgbrowse/internal/config"
	"github.com/joestump/msgbrowse/internal/devices"
	"github.com/joestump/msgbrowse/internal/devices/listener"
	"github.com/joestump/msgbrowse/internal/ingest"
	"github.com/joestump/msgbrowse/internal/store"
	"github.com/joestump/msgbrowse/internal/web"
	"github.com/spf13/cobra"
)

func newServeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the local HTMX web UI",
		Long: "serve runs the server-rendered HTMX web UI. It binds to loopback by\n" +
			"default; the UI has no authentication, so only expose it on a non-loopback\n" +
			"address behind your own access control.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			addr, err := resolveListenAddr(cmd, cfg.ListenAddr)
			if err != nil {
				return err
			}
			cfg.ListenAddr = addr

			st, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()

			// Signals cancel the context for graceful shutdown.
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			if cfg.IngestOnStart {
				if err := ingestOnStart(ctx, st, cfg); err != nil {
					slog.Warn("ingest-on-start failed; serving existing data", "error", err)
				}
			}

			srv, err := web.NewServer(st, cfg, slog.Default())
			if err != nil {
				return err
			}

			// Device sync (ADR-0018): with device_sync.enabled the sync
			// listener runs beside the web UI as a context-managed worker;
			// disabled (the default) means no second socket exists at all.
			devSync, err := startDeviceSync(ctx, cfg, st)
			if err != nil {
				return err
			}

			// Convenience for local use: open the UI in the default browser once
			// the listener is up. Best-effort and easily disabled (--open=false)
			// for headless/server runs.
			if open, _ := cmd.Flags().GetBool("open"); open {
				go openWhenReady(ctx, cfg.ListenAddr, slog.Default())
			}
			err = srv.Run(ctx, cfg.ListenAddr)

			// Wait for the sync listener's graceful drain (SPEC-0011
			// "Concurrency Safety": no leaked workers on shutdown).
			if devSync != nil {
				if derr := devSync.Wait(); derr != nil && err == nil {
					err = derr
				}
			}
			return err
		},
	}
	cmd.Flags().String("listen-addr", "", "full listen address host:port (overrides --host/--port and config)")
	cmd.Flags().String("host", "", "bind host (e.g. 127.0.0.1 or 0.0.0.0); default keeps the configured host")
	cmd.Flags().Int("port", 0, "bind port (e.g. 8888); default keeps the configured port")
	cmd.Flags().Bool("open", true, "open the UI in your default browser on start (use --open=false for headless)")
	return cmd
}

// resolveListenAddr layers the serve address flags over the configured default:
// --listen-addr replaces the whole address; otherwise --host / --port override
// just those parts of the configured host:port. Returns the final host:port.
func resolveListenAddr(cmd *cobra.Command, configured string) (string, error) {
	if la, _ := cmd.Flags().GetString("listen-addr"); la != "" {
		return la, nil
	}
	host, port, err := net.SplitHostPort(configured)
	if err != nil {
		return "", fmt.Errorf("invalid configured listen_addr %q: %w", configured, err)
	}
	if h, _ := cmd.Flags().GetString("host"); h != "" {
		host = h
	}
	if p, _ := cmd.Flags().GetInt("port"); p != 0 {
		if p < 1 || p > 65535 {
			return "", fmt.Errorf("invalid --port %d (want 1-65535)", p)
		}
		port = strconv.Itoa(p)
	}
	return net.JoinHostPort(host, port), nil
}

// deviceSyncWorker is a running device-sync listener started by
// startDeviceSync: its bound address and a Wait that blocks until the
// listener has fully drained.
type deviceSyncWorker struct {
	// Addr is the bound host:port (useful when the config asked for :0).
	Addr string
	done <-chan error
}

// Wait blocks until the listener worker exits and returns its error.
func (w *deviceSyncWorker) Wait() error { return <-w.done }

// startDeviceSync starts the device-sync listener as a context-managed
// worker when device_sync.enabled is true. With device sync disabled — the
// default — it returns (nil, nil) and creates NO socket, keeping the
// process's socket inventory exactly the loopback web UI (SPEC-0011
// "Default config exposes nothing new").
//
// The serve-embedded listener starts with NO pairing window: pairing windows
// are opened by `msgbrowse devices pair` today (the settings-page flow rides
// the SPEC-0010 story), so every unpinned certificate is rejected at the TLS
// layer for the whole life of this listener.
func startDeviceSync(ctx context.Context, cfg *config.Config, st *store.Store) (*deviceSyncWorker, error) {
	if !cfg.DeviceSync.Enabled {
		return nil, nil
	}
	name := deviceName(cfg)
	id, created, err := devices.LoadOrCreateIdentity(devices.IdentityDir(cfg.DataDir), name)
	if err != nil {
		return nil, err
	}
	if created {
		slog.Info("generated device-sync identity", "fingerprint", id.Fingerprint())
	}
	logger := newCharmLogger(cfg.LogLevel)
	l := &listener.Listener{
		Identity: id,
		Importer: &devices.Importer{
			DeviceName: name,
			Sources:    importedSources(cfg),
			Store:      st,
			Logger:     logger,
		},
		Registry: storeRegistry{st},
		Addr:     cfg.DeviceSync.ListenAddr,
		Logger:   logger,
	}
	// Bind eagerly and fail fast: the operator explicitly enabled the
	// listener, so a port conflict should abort serve, not degrade silently.
	ln, err := l.Listen(ctx)
	if err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		err := l.Serve(ctx, ln)
		if err != nil && ctx.Err() == nil {
			slog.Error("device-sync listener failed", "error", err)
		}
		done <- err
	}()
	return &deviceSyncWorker{Addr: ln.Addr().String(), done: done}, nil
}

// ingestOnStart runs a best-effort ingest pass before serving, when configured
// and an archive is available. The store handle from serve is reused; opening a
// second connection to the same SQLite file works (WAL handles it) but muddles
// ownership.
func ingestOnStart(ctx context.Context, st *store.Store, cfg *config.Config) error {
	if err := requireArchive(cfg); err != nil {
		return err
	}
	_, err := ingest.Run(ctx, st, ingest.Options{ArchiveRoot: cfg.ArchiveRoot})
	return err
}
