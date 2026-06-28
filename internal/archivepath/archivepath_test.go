package archivepath

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/joestump/msgbrowse/internal/source"
)

func TestResolve(t *testing.T) {
	const (
		sig = "/archive"
		im  = "/imsg"
	)
	cases := []struct {
		name           string
		src, conv, rel string
		want           string // exact expected path (empty = don't check exact)
		ok             bool
		base           string // when set, result must stay under this base (containment)
	}{
		{name: "signal", src: source.Signal, conv: "Harper", rel: "media/cat.jpg", want: "/archive/export/Harper/media/cat.jpg", ok: true},
		{name: "imessage flat", src: source.IMessage, conv: "+1555", rel: "attachments/AB/IMG.HEIC", want: "/imsg/attachments/AB/IMG.HEIC", ok: true},
		// Traversal is NOT rejected — it's neutralized to a path contained under base.
		{name: "signal traversal contained", src: source.Signal, conv: "Harper", rel: "../../etc/passwd", ok: true, base: "/archive/export/Harper"},
		{name: "imessage traversal contained", src: source.IMessage, conv: "x", rel: "../../etc/passwd", ok: true, base: "/imsg"},
		{name: "empty rel", src: source.Signal, conv: "Harper", rel: "", ok: false},
		{name: "unknown source falls to signal", src: "", conv: "Harper", rel: "media/x.png", want: "/archive/export/Harper/media/x.png", ok: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Resolve(c.src, sig, im, c.conv, c.rel)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (got %q)", ok, c.ok, got)
			}
			if !ok {
				return
			}
			slash := filepath.ToSlash(got)
			if c.want != "" && slash != c.want {
				t.Errorf("Resolve = %q, want %q", slash, c.want)
			}
			if c.base != "" && !strings.HasPrefix(slash, c.base) {
				t.Errorf("traversal escaped base: %q not under %q", slash, c.base)
			}
		})
	}
}

func TestResolveSignalNeedsArchiveRoot(t *testing.T) {
	if _, ok := Resolve(source.Signal, "", "/imsg", "Harper", "media/x.jpg"); ok {
		t.Error("signal with empty archiveRoot should fail")
	}
}
