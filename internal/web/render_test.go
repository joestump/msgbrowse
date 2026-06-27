package web

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderBody(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		contains []string
		excludes []string
	}{
		{
			name:     "escapes html",
			in:       "hello <script>alert(1)</script>",
			contains: []string{"&lt;script&gt;"},
			excludes: []string{"<script>"},
		},
		{
			name:     "drops image markdown",
			in:       "look ![a cat](media/cat.jpg)",
			contains: []string{"look "},
			excludes: []string{"media/cat.jpg", "![", "<img"},
		},
		{
			name:     "linkifies bare url and re-escapes trailing punctuation",
			in:       "see https://example.com/x.",
			contains: []string{`href="https://example.com/x"`, ">https://example.com/x<", "/a>."},
		},
		{
			name:     "markdown link to url becomes anchor with text",
			in:       "[menu](https://example.com/menu)",
			contains: []string{`href="https://example.com/menu"`, ">menu<"},
		},
		{
			name:     "markdown link to media is dropped",
			in:       "[lease.pdf](media/lease.pdf)",
			excludes: []string{"media/lease.pdf", "<a"},
		},
		{
			name:     "newlines become br",
			in:       "line1\nline2",
			contains: []string{"line1<br>line2"},
		},
		{
			name:     "anchors carry noopener noreferrer nofollow",
			in:       "https://example.com",
			contains: []string{`rel="noopener noreferrer nofollow"`, `target="_blank"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(renderBody(tt.in))
			for _, c := range tt.contains {
				if !strings.Contains(got, c) {
					t.Errorf("renderBody(%q) = %q, want contains %q", tt.in, got, c)
				}
			}
			for _, x := range tt.excludes {
				if strings.Contains(got, x) {
					t.Errorf("renderBody(%q) = %q, should not contain %q", tt.in, got, x)
				}
			}
		})
	}
}

func TestMediaURLEscaping(t *testing.T) {
	got := mediaURL(3, "media/holiday photo.jpg")
	if got != "/media/3/media/holiday%20photo.jpg" {
		t.Errorf("mediaURL = %q", got)
	}
}

func TestHumanSize(t *testing.T) {
	tests := map[int64]string{
		0:          "0 B",
		512:        "512 B",
		1024:       "1.0 KB",
		1536:       "1.5 KB",
		1048576:    "1.0 MB",
		1073741824: "1.0 GB",
	}
	for n, want := range tests {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestSafeMediaPath(t *testing.T) {
	root := "/archive"
	base := filepath.Join(root, "export", "Harper")

	t.Run("normal path", func(t *testing.T) {
		got, ok := safeMediaPath(root, "Harper", "media/cat.jpg")
		if !ok {
			t.Fatal("expected ok")
		}
		if got != filepath.Join(base, "media", "cat.jpg") {
			t.Errorf("path = %q", got)
		}
	})

	t.Run("traversal is contained within base", func(t *testing.T) {
		// Leading-slash anchoring neutralizes ".." so the result can never escape
		// the conversation directory.
		got, ok := safeMediaPath(root, "Harper", "../../../etc/passwd")
		if ok && !strings.HasPrefix(got, base) {
			t.Errorf("traversal escaped base: %q", got)
		}
	})

	t.Run("empty inputs rejected", func(t *testing.T) {
		if _, ok := safeMediaPath("", "Harper", "media/x"); ok {
			t.Error("empty archive root should be rejected")
		}
		if _, ok := safeMediaPath(root, "Harper", ""); ok {
			t.Error("empty rel path should be rejected")
		}
	})
}
