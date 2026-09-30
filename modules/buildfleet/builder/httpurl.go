package builder

import (
	"fmt"
	"net/url"
)

// NormalizeHTTPURL validates a runtime-mode http source URL: scheme must
// be http(s). The fragment is dropped — dockerfile.v0 parses "#ref:subdir"
// fragments off context URLs (git semantics) and would misread one.
func NormalizeHTTPURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("builder: runtime http mode requires http(s) url, got %q", rawURL)
	}
	u.Fragment = ""
	u.RawFragment = ""
	return u.String(), nil
}
