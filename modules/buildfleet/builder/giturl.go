package builder

import (
	"fmt"
	"net/url"
	"strings"
)

// NormalizeGitURL turns a git source into the remote-context URL form
// dockerfile.v0 parses: "<url>#<ref>:<subdir>". Plain https URLs not ending
// in .git (and not github.com, which buildkit special-cases) would be
// misdetected as an HTTP tarball context, so .git is appended. Local paths
// and other schemes are rejected: runtime-side fetch needs a remote
// buildkitd can reach.
func NormalizeGitURL(rawURL, ref, subdir string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("builder: git url: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
		if !strings.HasSuffix(u.Path, ".git") && !strings.HasPrefix(u.Host, "github.com") {
			u.Path += ".git"
		}
	case "ssh":
		// git-style scp syntax (git@host:path) parses with empty scheme
	default:
		return "", fmt.Errorf("builder: runtime git mode requires http(s)/ssh url, got %q", rawURL)
	}
	out := u.String()
	if frag := strings.TrimPrefix(joinRefSubdir(ref, subdir), "#"); frag != "" {
		out += "#" + frag
	}
	return out, nil
}

// NormalizeScpGitURL accepts git@host:path URLs (no scheme) by rewriting to
// ssh://; returns ok=false when the URL has neither scheme nor scp shape.
func NormalizeScpGitURL(rawURL string) (string, bool) {
	if at := strings.Index(rawURL, "@"); at > 0 && strings.Contains(rawURL[at:], ":") {
		return "ssh://" + strings.Replace(rawURL, ":", "/", 1), true
	}
	return rawURL, false
}

func joinRefSubdir(ref, subdir string) string {
	switch {
	case ref != "" && subdir != "":
		return "#" + ref + ":" + subdir
	case ref != "":
		return "#" + ref
	case subdir != "":
		return "#:" + subdir
	default:
		return ""
	}
}
