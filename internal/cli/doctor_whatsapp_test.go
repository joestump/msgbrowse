package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joestump/msgbrowse/internal/config"
	"github.com/joestump/msgbrowse/internal/whatsapp"
)

func TestClassifyWhatsAppMedia(t *testing.T) {
	const root = "/wapp"
	inside := filepath.Join(root, "WhatsApp", "Media", "img.jpg")

	cases := []struct {
		name     string
		ref      whatsapp.MediaRef
		existing map[string]bool
		want     whatsappMediaClass
	}{
		{
			name:     "relative present",
			ref:      whatsapp.MediaRef{MediaBase: "", Data: "WhatsApp/Media/img.jpg"},
			existing: map[string]bool{inside: true},
			want:     whatsappMediaPresent,
		},
		{
			name: "relative missing",
			ref:  whatsapp.MediaRef{MediaBase: "", Data: "WhatsApp/Media/img.jpg"},
			want: whatsappMediaMissing,
		},
		{
			name:     "relative media_base prefix present",
			ref:      whatsapp.MediaRef{MediaBase: "WhatsApp/Media/", Data: "img.jpg"},
			existing: map[string]bool{inside: true},
			want:     whatsappMediaPresent,
		},
		{
			// The real-export signature: an absolute media_base pointing at the
			// raw device dump instead of the copied media under the root.
			name: "absolute media_base outside root",
			ref:  whatsapp.MediaRef{MediaBase: "/tank/raw/Message/Media/", Data: "img.jpg"},
			want: whatsappMediaOutside,
		},
		{
			name:     "absolute media_base under root is relativized",
			ref:      whatsapp.MediaRef{MediaBase: root + "/WhatsApp/Media/", Data: "img.jpg"},
			existing: map[string]bool{inside: true},
			want:     whatsappMediaPresent,
		},
		{
			name: "absolute media_base under root but file gone",
			ref:  whatsapp.MediaRef{MediaBase: root + "/WhatsApp/Media/", Data: "img.jpg"},
			want: whatsappMediaMissing,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyWhatsAppMedia(root, c.ref, statFn(c.existing))
			if got != c.want {
				t.Errorf("classifyWhatsAppMedia = %v, want %v", got, c.want)
			}
		})
	}
}

func TestWhatsAppMediaVerdict(t *testing.T) {
	cases := []struct {
		name       string
		device     string
		stats      whatsappMediaStats
		wantStatus checkStatus
		wantInHint string
	}{
		{
			name:       "all present passes",
			stats:      whatsappMediaStats{Present: 10},
			wantStatus: statusPass,
		},
		{
			name:       "majority outside fails",
			device:     whatsapp.DeviceIOS,
			stats:      whatsappMediaStats{Present: 1, Outside: 5},
			wantStatus: statusFail,
			wantInHint: "Finder/iTunes",
		},
		{
			name:       "majority missing fails with media hint",
			device:     whatsapp.DeviceAndroid,
			stats:      whatsappMediaStats{Present: 1, Missing: 5},
			wantStatus: statusFail,
			wantInHint: "64-digit",
		},
		{
			name:       "few outside warns",
			stats:      whatsappMediaStats{Present: 9, Outside: 1},
			wantStatus: statusWarn,
			wantInHint: "outside the archive root",
		},
		{
			name:       "few missing warns",
			stats:      whatsappMediaStats{Present: 9, Missing: 1},
			wantStatus: statusWarn,
			wantInHint: "missing under the root",
		},
		{
			name:       "empty passes",
			stats:      whatsappMediaStats{},
			wantStatus: statusPass,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, hint := whatsappMediaVerdict(c.device, &c.stats)
			if status != c.wantStatus {
				t.Errorf("status = %v, want %v (hint %q)", status, c.wantStatus, hint)
			}
			if c.wantInHint != "" && !strings.Contains(hint, c.wantInHint) {
				t.Errorf("hint %q missing %q", hint, c.wantInHint)
			}
			// Every non-pass verdict must tell the user how to recover.
			if status != statusPass && !strings.Contains(hint, "import --full") {
				t.Errorf("hint %q should mention `import --full`", hint)
			}
		})
	}
}

func TestAbsMediaBasesOutside(t *testing.T) {
	const root = "/wapp"
	n, example := absMediaBasesOutside(root, map[string]int{
		"/tank/raw/Message/Media/": 2, // absolute, outside → counted
		"/wapp/WhatsApp/":          1, // absolute but under the root → fine
		"WhatsApp/":                4, // relative → fine
	})
	if n != 2 {
		t.Errorf("count = %d, want 2 (only the outside-root chats)", n)
	}
	if example != "/tank/raw/Message/Media/" {
		t.Errorf("example = %q", example)
	}
	if n, _ := absMediaBasesOutside(root, nil); n != 0 {
		t.Errorf("empty map count = %d, want 0", n)
	}
}

func TestWhatsAppBackupHint(t *testing.T) {
	if h := whatsappBackupHint(whatsapp.DeviceIOS); !strings.Contains(h, "Finder/iTunes") {
		t.Errorf("ios hint %q should mention the local Finder/iTunes backup", h)
	}
	if h := whatsappBackupHint(whatsapp.DeviceAndroid); !strings.Contains(h, "64-digit") {
		t.Errorf("android hint %q should mention the 64-digit key", h)
	}
	h := whatsappBackupHint("")
	if !strings.Contains(h, "Finder/iTunes") || !strings.Contains(h, "64-digit") {
		t.Errorf("unknown-device hint %q should mention both platform paths", h)
	}
}

// TestCheckWhatsAppArchive drives the whole check against real temp trees —
// the REQ-0009-009 table: unset root, missing root, missing JSON, unparseable
// JSON, healthy export, reference-only export (absolute media_base), and
// media-not-copied export.
func TestCheckWhatsAppArchive(t *testing.T) {
	writeExport := func(t *testing.T, doc string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, whatsapp.ResultFile), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	const healthyDoc = `{
		"15550001111@s.whatsapp.net": {"name": "A", "type": "ios", "media_base": "", "messages": {
			"1": {"from_me": false, "timestamp": 1748768400, "data": "WhatsApp/img.jpg", "media": true, "mime": "image/jpeg"}
		}}
	}`
	const refOnlyDoc = `{
		"15550001111@s.whatsapp.net": {"name": "A", "type": "ios", "media_base": "/somewhere/else/", "messages": {
			"1": {"from_me": false, "timestamp": 1748768400, "data": "img.jpg", "media": true, "mime": "image/jpeg"}
		}}
	}`

	cases := []struct {
		name      string
		root      func(t *testing.T) string
		wantFails int
		wantWarns int
		wantOut   []string
	}{
		{
			name:      "unset root warns",
			root:      func(*testing.T) string { return "" },
			wantWarns: 1,
			wantOut:   []string{"whatsapp_archive_root is not set"},
		},
		{
			name:      "missing root fails",
			root:      func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope") },
			wantFails: 1,
			wantOut:   []string{"whatsapp_archive_root"},
		},
		{
			name:      "no result.json warns with platform hints",
			root:      func(t *testing.T) string { return t.TempDir() },
			wantWarns: 1,
			wantOut:   []string{"has no result.json", "wtsexporter", "Finder/iTunes", "64-digit"},
		},
		{
			name: "unparseable json fails",
			root: func(t *testing.T) string { return writeExport(t, `[]`) },
			// 1 fail (parse) — no media checks can run.
			wantFails: 1,
			wantOut:   []string{"could not parse"},
		},
		{
			name: "healthy export passes",
			root: func(t *testing.T) string {
				dir := writeExport(t, healthyDoc)
				if err := os.MkdirAll(filepath.Join(dir, "WhatsApp"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "WhatsApp", "img.jpg"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			wantOut: []string{"1 chats, 1 messages", "1 ok, 0 outside the root, 0 missing"},
		},
		{
			name: "absolute media_base outside the root fails twice",
			root: func(t *testing.T) string { return writeExport(t, refOnlyDoc) },
			// The chat-level media_base check AND the sampled-reference check
			// both fail: nothing this export references is browsable.
			wantFails: 2,
			wantOut:   []string{"absolute media_base outside the archive", "/somewhere/else/", "0 ok, 1 outside the root"},
		},
		{
			name: "media directory not copied fails",
			root: func(t *testing.T) string { return writeExport(t, healthyDoc) },
			// result.json is fine but WhatsApp/img.jpg was never copied.
			wantFails: 1,
			wantOut:   []string{"0 ok, 0 outside the root, 1 missing", "media directory was not copied"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			r := &report{w: out}
			checkWhatsAppArchive(r, &config.Config{WhatsAppArchiveRoot: c.root(t)})
			if r.fails != c.wantFails {
				t.Errorf("fails = %d, want %d\n%s", r.fails, c.wantFails, out.String())
			}
			if r.warnings != c.wantWarns {
				t.Errorf("warnings = %d, want %d\n%s", r.warnings, c.wantWarns, out.String())
			}
			for _, want := range c.wantOut {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output missing %q:\n%s", want, out.String())
				}
			}
		})
	}
}
