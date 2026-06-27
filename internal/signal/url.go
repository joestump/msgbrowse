package signal

import (
	"net/url"
	"strings"
)

// Domain extracts the lowercased host (without a leading "www.") from a URL, for
// grouping links by site. It returns "" if the host cannot be determined.
func Domain(rawurl string) string {
	u, err := url.Parse(rawurl)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	return strings.TrimPrefix(host, "www.")
}
