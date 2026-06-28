package cli

import "testing"

func TestBrowserURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8787": "http://127.0.0.1:8787",
		"0.0.0.0:8787":   "http://127.0.0.1:8787", // wildcard → loopback for the browser
		":8787":          "http://127.0.0.1:8787", // empty host → loopback
		"[::]:8787":      "http://127.0.0.1:8787",
		"192.168.1.5:80": "http://192.168.1.5:80",
		"localhost:9000": "http://localhost:9000",
	}
	for in, want := range cases {
		if got := browserURL(in); got != want {
			t.Errorf("browserURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBrowserOpenCommand(t *testing.T) {
	cases := []struct {
		goos     string
		wantName string
		wantOK   bool
	}{
		{"darwin", "open", true},
		{"linux", "xdg-open", true},
		{"windows", "rundll32", true},
		{"plan9", "", false},
	}
	for _, c := range cases {
		name, args, ok := browserOpenCommand(c.goos, "http://127.0.0.1:8787")
		if ok != c.wantOK || name != c.wantName {
			t.Errorf("browserOpenCommand(%q) = (%q, %v, %v), want name %q ok %v", c.goos, name, args, ok, c.wantName, c.wantOK)
		}
		if ok {
			found := false
			for _, a := range args {
				if a == "http://127.0.0.1:8787" {
					found = true
				}
			}
			if !found {
				t.Errorf("browserOpenCommand(%q) args %v missing the url", c.goos, args)
			}
		}
	}
}
