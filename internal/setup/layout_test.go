package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joestump/msgbrowse/internal/source"
)

func TestManagedRoot(t *testing.T) {
	cases := []struct {
		name    string
		dataDir string
		src     string
		want    string
		wantErr bool
	}{
		{
			name:    "signal",
			dataDir: "/data",
			src:     source.Signal,
			want:    filepath.Join("/data", "archives", "signal"),
		},
		{
			name:    "imessage",
			dataDir: "/data",
			src:     source.IMessage,
			want:    filepath.Join("/data", "archives", "imessage"),
		},
		{
			name:    "whatsapp",
			dataDir: "/data",
			src:     source.WhatsApp,
			want:    filepath.Join("/data", "archives", "whatsapp"),
		},
		{
			name:    "unknown source is rejected (no guessed path)",
			dataDir: "/data",
			src:     "../../etc",
			wantErr: true,
		},
		{
			name:    "empty source is rejected",
			dataDir: "/data",
			src:     "",
			wantErr: true,
		},
		{
			name:    "empty data dir is rejected",
			dataDir: "",
			src:     source.Signal,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ManagedRoot(c.dataDir, c.src)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("ManagedRoot(%q, %q) = %q, want %q", c.dataDir, c.src, got, c.want)
			}
		})
	}
}

func TestComputeLayout(t *testing.T) {
	layout, err := ComputeLayout("/data")
	if err != nil {
		t.Fatal(err)
	}
	if layout.DataDir != "/data" {
		t.Errorf("DataDir = %q", layout.DataDir)
	}
	if layout.ArchivesDir != filepath.Join("/data", "archives") {
		t.Errorf("ArchivesDir = %q", layout.ArchivesDir)
	}
	if len(layout.Roots) != len(source.All) {
		t.Fatalf("Roots has %d entries, want %d", len(layout.Roots), len(source.All))
	}
	for _, src := range source.All {
		want := filepath.Join("/data", "archives", src)
		if layout.Roots[src] != want {
			t.Errorf("Roots[%q] = %q, want %q", src, layout.Roots[src], want)
		}
	}

	if _, err := ComputeLayout(""); err == nil {
		t.Error("ComputeLayout(\"\") should error, not point at the filesystem root")
	}
}

func TestProvisionCreatesLayout(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "msgbrowse") // does not exist yet
	layout, err := Provision(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	// The data dir, the archives dir, and every per-source root must exist.
	mustDir(t, layout.DataDir)
	mustDir(t, layout.ArchivesDir)
	for _, src := range source.All {
		mustDir(t, layout.Roots[src])
	}

	// Owner-only permissions on the managed roots (they hold personal exports).
	info, err := os.Stat(layout.Roots[source.Signal])
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != managedDirPerm {
		t.Errorf("managed root perm = %o, want %o", perm, managedDirPerm)
	}
}

func TestProvisionIsIdempotent(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "msgbrowse")
	if _, err := Provision(dataDir); err != nil {
		t.Fatal(err)
	}
	// Drop a file into a managed root, then provision again: the second call is
	// a no-op that must not error or wipe existing content (the returning-launch
	// path from SPEC-0013).
	marker := filepath.Join(dataDir, "archives", source.Signal, "keep.txt")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Provision(dataDir); err != nil {
		t.Fatalf("second Provision errored: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("idempotent Provision removed existing content: %v", err)
	}
}

func TestProvisionRejectsEmptyDataDir(t *testing.T) {
	if _, err := Provision(""); err == nil {
		t.Error("Provision(\"\") should error")
	}
}

func mustDir(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected dir %q: %v", path, err)
	}
	if !info.IsDir() {
		t.Fatalf("%q is not a directory", path)
	}
}
