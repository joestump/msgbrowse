package onboardsvc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/joestump/msgbrowse/internal/config"
	"github.com/joestump/msgbrowse/internal/onboard"
	"github.com/joestump/msgbrowse/internal/source"
	"github.com/joestump/msgbrowse/internal/store"
)

// discardLogger is a slog logger that drops output, so importer logs don't noise
// the test output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestPathResolverOverride: an explicit override is returned verbatim, without a
// $PATH lookup (so a configured absolute path always wins).
func TestPathResolverOverride(t *testing.T) {
	r := PathToolResolver{SignalBin: "/opt/tools/sigexport"}
	got, err := r.ResolveTool(context.Background(), source.Signal)
	if err != nil {
		t.Fatalf("ResolveTool: %v", err)
	}
	if got != "/opt/tools/sigexport" {
		t.Fatalf("override path = %q, want /opt/tools/sigexport", got)
	}
}

// TestPathResolverMissingToolSentinel: a source with no override and no tool on
// PATH returns onboard.ErrToolMissing so Enable is a clear "unavailable".
func TestPathResolverMissingToolSentinel(t *testing.T) {
	// Point PATH at an empty dir so the default exporter names cannot resolve.
	t.Setenv("PATH", t.TempDir())
	r := PathToolResolver{}
	_, err := r.ResolveTool(context.Background(), source.IMessage)
	if !errors.Is(err, onboard.ErrToolMissing) {
		t.Fatalf("ResolveTool(no tool) error = %v, want ErrToolMissing", err)
	}
}

// TestPathResolverUnknownSource: a non-enum source is rejected.
func TestPathResolverUnknownSource(t *testing.T) {
	r := PathToolResolver{}
	if _, err := r.ResolveTool(context.Background(), "bogus"); !errors.Is(err, onboard.ErrUnknownSource) {
		t.Fatalf("ResolveTool(bogus) error = %v, want ErrUnknownSource", err)
	}
}

// TestPathResolverFromConfig maps the config exporter-bin keys onto the resolver.
func TestPathResolverFromConfig(t *testing.T) {
	cfg := &config.Config{
		SignalExportBin:     "/a/sigexport",
		IMessageExporterBin: "/a/imessage-exporter",
		WhatsAppExporterBin: "/a/wtsexporter",
	}
	r := PathResolverFromConfig(cfg)
	for src, want := range map[string]string{
		source.Signal:   "/a/sigexport",
		source.IMessage: "/a/imessage-exporter",
		source.WhatsApp: "/a/wtsexporter",
	} {
		got, err := r.ResolveTool(context.Background(), src)
		if err != nil {
			t.Fatalf("ResolveTool(%s): %v", src, err)
		}
		if got != want {
			t.Fatalf("ResolveTool(%s) = %q, want %q", src, got, want)
		}
	}
}

// TestStoreImporterDispatchesSignal: the storeImporter routes a Signal managed
// root through internal/ingest and lands conversations in the store — proving the
// import reuses the existing importer (SPEC-0013). Built via the exported Build
// path so the whole wiring is exercised.
func TestStoreImporterDispatchesSignal(t *testing.T) {
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, store.DBFileName))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Lay down a managed Signal archive at <dataDir>/archives/signal/export/Alice.
	root := filepath.Join(dataDir, "archives", "signal")
	convDir := filepath.Join(root, "export", "Alice")
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatal(err)
	}
	chat := "[2022-01-01 10:00:00] Alice: hello\n[2022-01-01 10:01:00] Me: hi\n"
	if err := os.WriteFile(filepath.Join(convDir, "chat.md"), []byte(chat), 0o644); err != nil {
		t.Fatal(err)
	}

	im := &storeImporter{st: st, cfg: &config.Config{DataDir: dataDir}, log: discardLogger()}
	res, err := im.Import(context.Background(), source.Signal, root)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.MessagesAdded != 2 || res.ConversationsChanged != 1 {
		t.Fatalf("import result = %+v, want 2 messages / 1 conversation", res)
	}
	convs, err := st.ListConversations(context.Background())
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(convs) != 1 || convs[0].Source != source.Signal {
		t.Fatalf("store has %d conversations (want 1 signal): %+v", len(convs), convs)
	}
}
