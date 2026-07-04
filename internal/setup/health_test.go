package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joestump/msgbrowse/internal/archivepath"
	"github.com/joestump/msgbrowse/internal/source"
	"github.com/joestump/msgbrowse/internal/whatsapp"
)

// statExisting reports the given paths as present, everything else missing, so
// the classifiers run without touching disk.
func statExisting(existing map[string]bool) func(string) (os.FileInfo, error) {
	return func(p string) (os.FileInfo, error) {
		if existing[p] {
			return fileInfo{}, nil
		}
		return nil, os.ErrNotExist
	}
}

func TestClassifyAttachment(t *testing.T) {
	const (
		archiveRoot  = "/archive"
		imessageRoot = "/imsg"
		whatsappRoot = "/wa"
		conv         = "Alice"
	)
	roots := archivepath.Roots{Signal: archiveRoot, IMessage: imessageRoot, WhatsApp: whatsappRoot}
	imsgAbs := filepath.Join(imessageRoot, "attachments", "AB", "IMG.HEIC")
	sigAbs := filepath.Join(archiveRoot, "export", conv, "media", "pic.jpg")

	cases := []struct {
		name     string
		src      string
		rel      string
		existing map[string]bool
		want     AttachmentClass
	}{
		{"imessage absolute", source.IMessage, "/Users/joe/Library/Messages/Attachments/IMG.HEIC", nil, AttachAbsolute},
		{"imessage present", source.IMessage, "attachments/AB/IMG.HEIC", map[string]bool{imsgAbs: true}, AttachPresent},
		{"imessage missing", source.IMessage, "attachments/AB/IMG.HEIC", nil, AttachMissing},
		{"signal present", source.Signal, "media/pic.jpg", map[string]bool{sigAbs: true}, AttachPresent},
		{"signal missing", source.Signal, "media/gone.jpg", nil, AttachMissing},
		{"signal absolute", source.Signal, "/var/leaked.jpg", nil, AttachAbsolute},
		{"unset root is missing", source.Signal, "media/pic.jpg", nil, AttachMissing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := roots
			if c.name == "unset root is missing" {
				r = archivepath.Roots{}
			}
			got := ClassifyAttachment(c.src, r, conv, c.rel, statExisting(c.existing))
			if got != c.want {
				t.Errorf("ClassifyAttachment = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAttachmentVerdict(t *testing.T) {
	cases := []struct {
		name       string
		src        string
		stats      AttachmentStats
		wantHealth Health
		wantHint   bool
	}{
		{"all present", source.IMessage, AttachmentStats{Present: 50}, HealthOK, false},
		{"empty", source.IMessage, AttachmentStats{}, HealthOK, false},
		{"imessage majority absolute", source.IMessage, AttachmentStats{Present: 2, Absolute: 98}, HealthProblem, true},
		{"imessage minority absolute", source.IMessage, AttachmentStats{Present: 90, Absolute: 10}, HealthWarn, true},
		{"signal majority absolute", source.Signal, AttachmentStats{Present: 1, Absolute: 9}, HealthProblem, true},
		{"mostly missing", source.IMessage, AttachmentStats{Present: 10, Missing: 90}, HealthWarn, true},
		{"few missing", source.Signal, AttachmentStats{Present: 95, Missing: 5}, HealthWarn, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.stats
			h, hint := AttachmentVerdict(c.src, &s)
			if h != c.wantHealth {
				t.Errorf("health = %v, want %v", h, c.wantHealth)
			}
			if c.wantHint != (hint != "") {
				t.Errorf("hint presence = %v (%q), want %v", hint != "", hint, c.wantHint)
			}
		})
	}
}

func TestAttachmentVerdictImessageHintMentionsCopyMode(t *testing.T) {
	s := AttachmentStats{Present: 1, Absolute: 99}
	h, hint := AttachmentVerdict(source.IMessage, &s)
	if h != HealthProblem {
		t.Fatalf("health = %v, want HealthProblem", h)
	}
	for _, want := range []string{"copy-mode", "copy-method", "import --full"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q missing %q", hint, want)
		}
	}
}

func TestClassifyArchiveRoot(t *testing.T) {
	mk := func(t *testing.T, base string, rels ...string) {
		t.Helper()
		for _, r := range rels {
			if err := os.MkdirAll(filepath.Join(base, r), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Run("correct root", func(t *testing.T) {
		root := t.TempDir()
		mk(t, root, "export/Alice")
		if got := ClassifyArchiveRoot(root); got != ArchiveRootOK {
			t.Errorf("got %v, want ArchiveRootOK", got)
		}
	})
	t.Run("nested export/export", func(t *testing.T) {
		root := t.TempDir()
		mk(t, root, "export/export/Alice")
		if got := ClassifyArchiveRoot(root); got != ArchiveRootPointsAtExport {
			t.Errorf("got %v, want ArchiveRootPointsAtExport", got)
		}
	})
	t.Run("named export, no export subdir", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "export")
		mk(t, root, "Alice")
		if got := ClassifyArchiveRoot(root); got != ArchiveRootPointsAtExport {
			t.Errorf("got %v, want ArchiveRootPointsAtExport", got)
		}
	})
	t.Run("wrong directory", func(t *testing.T) {
		root := t.TempDir()
		mk(t, root, "random")
		if got := ClassifyArchiveRoot(root); got != ArchiveRootNoExport {
			t.Errorf("got %v, want ArchiveRootNoExport", got)
		}
	})
}

func TestClassifyWhatsAppMedia(t *testing.T) {
	const root = "/wapp"
	inside := filepath.Join(root, "WhatsApp", "Media", "img.jpg")
	cases := []struct {
		name     string
		ref      whatsapp.MediaRef
		existing map[string]bool
		want     WhatsAppMediaClass
	}{
		{"relative present", whatsapp.MediaRef{Data: "WhatsApp/Media/img.jpg"}, map[string]bool{inside: true}, WhatsAppMediaPresent},
		{"relative missing", whatsapp.MediaRef{Data: "WhatsApp/Media/img.jpg"}, nil, WhatsAppMediaMissing},
		{"media_base prefix present", whatsapp.MediaRef{MediaBase: "WhatsApp/Media/", Data: "img.jpg"}, map[string]bool{inside: true}, WhatsAppMediaPresent},
		{"absolute outside root", whatsapp.MediaRef{MediaBase: "/tank/raw/Message/Media/", Data: "img.jpg"}, nil, WhatsAppMediaOutside},
		{"absolute under root present", whatsapp.MediaRef{MediaBase: root + "/WhatsApp/Media/", Data: "img.jpg"}, map[string]bool{inside: true}, WhatsAppMediaPresent},
		{"absolute under root gone", whatsapp.MediaRef{MediaBase: root + "/WhatsApp/Media/", Data: "img.jpg"}, nil, WhatsAppMediaMissing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyWhatsAppMedia(root, c.ref, statExisting(c.existing))
			if got != c.want {
				t.Errorf("ClassifyWhatsAppMedia = %v, want %v", got, c.want)
			}
		})
	}
}

func TestWhatsAppMediaVerdict(t *testing.T) {
	cases := []struct {
		name       string
		device     string
		stats      WhatsAppMediaStats
		wantHealth Health
		wantInHint string
	}{
		{"all present", "", WhatsAppMediaStats{Present: 10}, HealthOK, ""},
		{"majority outside", whatsapp.DeviceIOS, WhatsAppMediaStats{Present: 1, Outside: 5}, HealthProblem, "Finder/iTunes"},
		{"majority missing", whatsapp.DeviceAndroid, WhatsAppMediaStats{Present: 1, Missing: 5}, HealthProblem, "64-digit"},
		{"few outside", "", WhatsAppMediaStats{Present: 9, Outside: 1}, HealthWarn, "outside the archive root"},
		{"few missing", "", WhatsAppMediaStats{Present: 9, Missing: 1}, HealthWarn, "missing under the root"},
		{"empty", "", WhatsAppMediaStats{}, HealthOK, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.stats
			h, hint := WhatsAppMediaVerdict(c.device, &s)
			if h != c.wantHealth {
				t.Errorf("health = %v, want %v (hint %q)", h, c.wantHealth, hint)
			}
			if c.wantInHint != "" && !strings.Contains(hint, c.wantInHint) {
				t.Errorf("hint %q missing %q", hint, c.wantInHint)
			}
			if h != HealthOK && !strings.Contains(hint, "import --full") {
				t.Errorf("non-ok hint %q should mention import --full", hint)
			}
		})
	}
}

func TestAbsMediaBasesOutside(t *testing.T) {
	const root = "/wapp"
	n, example := AbsMediaBasesOutside(root, map[string]int{
		"/tank/raw/Message/Media/": 2,
		"/wapp/WhatsApp/":          1,
		"WhatsApp/":                4,
	})
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	if example != "/tank/raw/Message/Media/" {
		t.Errorf("example = %q", example)
	}
	if n, _ := AbsMediaBasesOutside(root, nil); n != 0 {
		t.Errorf("empty map count = %d, want 0", n)
	}
}

func TestWhatsAppBackupHint(t *testing.T) {
	if h := WhatsAppBackupHint(whatsapp.DeviceIOS); !strings.Contains(h, "Finder/iTunes") {
		t.Errorf("ios hint %q", h)
	}
	if h := WhatsAppBackupHint(whatsapp.DeviceAndroid); !strings.Contains(h, "64-digit") {
		t.Errorf("android hint %q", h)
	}
	h := WhatsAppBackupHint("")
	if !strings.Contains(h, "Finder/iTunes") || !strings.Contains(h, "64-digit") {
		t.Errorf("unknown-device hint %q should mention both", h)
	}
}

func TestFraction(t *testing.T) {
	if got := Fraction(0, 0); got != 0 {
		t.Errorf("Fraction(0,0) = %v, want 0", got)
	}
	if got := Fraction(1, 2); got != 0.5 {
		t.Errorf("Fraction(1,2) = %v, want 0.5", got)
	}
}

func TestHealthString(t *testing.T) {
	cases := map[Health]string{HealthOK: "ok", HealthWarn: "warn", HealthProblem: "problem"}
	for h, want := range cases {
		if got := h.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", h, got, want)
		}
	}
}
