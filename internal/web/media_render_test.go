package web

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joestump/msgbrowse/internal/imageconv"
	"github.com/joestump/msgbrowse/internal/source"
)

// TestImgRenderable covers the gallery/transcript placeholder decision: web
// formats always render; a non-web format (HEIC) renders only once a transcoded
// JPEG derivative exists on disk.
func TestImgRenderable(t *testing.T) {
	srv, _, _ := newTestServer(t)

	if !srv.imgRenderable(source.Signal, "Harper", "media/cat.jpg") {
		t.Error("jpg should be renderable")
	}
	if srv.imgRenderable(source.Signal, "Harper", "media/IMG_0001.heic") {
		t.Error("heic with no derivative should NOT be renderable")
	}

	// Drop a fake derivative at the exact path the server will look for.
	abs, ok := srv.mediaFilePath(source.Signal, "Harper", "media/IMG_0001.heic")
	if !ok {
		t.Fatal("mediaFilePath failed to resolve")
	}
	d := imageconv.DerivedPath(srv.derivedDir, abs)
	if err := os.MkdirAll(filepath.Dir(d), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d, []byte("\xff\xd8\xff jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !srv.imgRenderable(source.Signal, "Harper", "media/IMG_0001.heic") {
		t.Error("heic WITH a derivative should be renderable")
	}
}
