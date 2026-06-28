package imageconv

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWebRenderableAndConvertible(t *testing.T) {
	web := []string{"a.jpg", "B.JPEG", "c.png", "d.gif", "e.webp", "f.bmp"}
	for _, n := range web {
		if !WebRenderable(n) {
			t.Errorf("WebRenderable(%q) = false, want true", n)
		}
		if Convertible(n) {
			t.Errorf("Convertible(%q) = true, want false", n)
		}
	}
	conv := []string{"x.heic", "Y.HEIC", "z.heif", "w.tiff", "v.tif"}
	for _, n := range conv {
		if !Convertible(n) {
			t.Errorf("Convertible(%q) = false, want true", n)
		}
		if WebRenderable(n) {
			t.Errorf("WebRenderable(%q) = true, want false", n)
		}
	}
	// Unknown / non-image extensions are neither.
	for _, n := range []string{"a.pdf", "b.txt", "c.svg", "noext"} {
		if WebRenderable(n) || Convertible(n) {
			t.Errorf("%q classified as an image", n)
		}
	}
}

func TestDerivedPath(t *testing.T) {
	if DerivedPath("", "/x/y.heic") != "" {
		t.Error("empty derivedDir should yield empty path")
	}
	d := "/data/derived"
	p1 := DerivedPath(d, "/archive/IMG_1.heic")
	p2 := DerivedPath(d, "/archive/IMG_2.heic")
	if p1 == p2 {
		t.Error("different sources should map to different derivatives")
	}
	if DerivedPath(d, "/archive/IMG_1.heic") != p1 {
		t.Error("DerivedPath must be deterministic for the same source")
	}
	if filepath.Dir(p1) != d {
		t.Errorf("derivative %q not under %q", p1, d)
	}
	if !strings.HasSuffix(p1, ".jpg") {
		t.Errorf("derivative %q should be a .jpg", p1)
	}
}

func TestDerivedDir(t *testing.T) {
	if got := DerivedDir("/data"); filepath.ToSlash(got) != "/data/derived" {
		t.Errorf("DerivedDir = %q", got)
	}
}
