package web

import (
	"io/fs"
	"strings"
	"testing"
)

// TestNoInlineStylesInTemplates enforces the CSP contract: the server sets
// `style-src 'self'` with no 'unsafe-inline', so inline style="" attributes (and
// <style> blocks) are blocked by the browser and silently do nothing. Any such
// attribute is a bug — styling must live in app.css via classes. This guard
// scans the embedded templates so the regression can't reach a browser.
func TestNoInlineStylesInTemplates(t *testing.T) {
	err := fs.WalkDir(templatesFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := templatesFS.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		content := string(b)
		if strings.Contains(content, "style=\"") || strings.Contains(content, "style='") {
			t.Errorf("%s contains an inline style attribute — forbidden by CSP (style-src 'self'); move it to a class in input.css", path)
		}
		if strings.Contains(content, "<style") {
			t.Errorf("%s contains a <style> block — forbidden by CSP; use app.css", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
}
