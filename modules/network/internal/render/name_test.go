package render

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// suffixFor recomputes the documented suffix so tests pin the algorithm
// instead of the implementation.
func suffixFor(t *testing.T, host string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(host))
	return hex.EncodeToString(sum[:4])
}

func TestResourceName(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		wantBase string // expected part before the hash suffix; "" means bare hash
	}{
		{"simple host", "api.example.com", "api-example-com"},
		{"case and surrounding space normalize", "  API.Example.COM  ", "api-example-com"},
		{"dots fold to dash", "aaa.bb.com", "aaa-bb-com"},
		{"hyphens fold like dots", "aaa-bb.com", "aaa-bb-com"},
		{"non-ascii folds", "api.例子.com", "api-com"},
		{"253-char host truncates to budget", strings.Repeat("a", 253), strings.Repeat("a", 54)},
		{"all illegal chars fall back to hash", "...___...", ""},
		{"empty host falls back to hash", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResourceName(tt.host)
			suffix := suffixFor(t, strings.ToLower(strings.TrimSpace(tt.host)))
			want := suffix
			if tt.wantBase != "" {
				want = tt.wantBase + "-" + suffix
			}
			if got != want {
				t.Errorf("ResourceName(%q) = %q, want %q", tt.host, got, want)
			}
			if len(got) > 63 {
				t.Errorf("name %q length %d, want <= 63", got, len(got))
			}
			if !dns1123Label.MatchString(got) {
				t.Errorf("name %q is not a valid DNS-1123 label", got)
			}
		})
	}
}

func TestResourceNameCaseNormalization(t *testing.T) {
	if ResourceName("API.Example.COM") != ResourceName("api.example.com") {
		t.Fatal("case must be normalized before deriving the name")
	}
}

// ServiceIngressName must stay a valid label within the limit, be stable per
// (host, app) and distinct across apps and hosts.
func TestServiceIngressName(t *testing.T) {
	longHost := strings.Repeat("a", 253)
	tests := []struct {
		name string
		host string
		app  string
	}{
		{"simple", "api.example.com", "web"},
		{"long host truncates", longHost, "web"},
		{"folded host", "aaa.bb.com", "api-gateway"},
		{"illegal host", "...___...", "web"},
	}
	seen := map[string]string{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ServiceIngressName(tt.host, tt.app)
			if len(got) > 63 {
				t.Errorf("name %q length %d, want <= 63", got, len(got))
			}
			if !dns1123Label.MatchString(got) {
				t.Errorf("name %q is not a valid DNS-1123 label", got)
			}
			if again := ServiceIngressName(tt.host, tt.app); again != got {
				t.Errorf("name not stable: %q vs %q", again, got)
			}
			if prev, dup := seen[got]; dup {
				t.Errorf("name %q collision between %s and %s", got, prev, tt.host+"/"+tt.app)
			}
			seen[got] = tt.host + "/" + tt.app
			if got == ResourceName(tt.host) {
				t.Errorf("service name %q must differ from the main name", got)
			}
		})
	}
	if ServiceIngressName("api.example.com", "web") == ServiceIngressName("api.example.com", "api") {
		t.Error("different apps derived the same name")
	}
	if ServiceIngressName("api.example.com", "web") == ServiceIngressName("other.example.com", "web") {
		t.Error("different hosts derived the same service name")
	}
}

// Hosts that sanitize to the same base must still derive distinct names:
// the hash covers the pre-sanitize string.
func TestResourceNameFoldCollision(t *testing.T) {
	a := ResourceName("aaa.bb.com")
	b := ResourceName("aaa-bb.com")
	if a == b {
		t.Fatalf("folded-equal hosts derived the same name %q", a)
	}
	if !strings.HasPrefix(a, "aaa-bb-com-") || !strings.HasPrefix(b, "aaa-bb-com-") {
		t.Errorf("names %q / %q must carry the folded base", a, b)
	}
}

func TestResourceNameLongHostKeepsSuffix(t *testing.T) {
	host := strings.Repeat("a", 253)
	name := ResourceName(host)
	if len(name) != 63 {
		t.Fatalf("length %d, want exactly 63", len(name))
	}
	if !strings.HasSuffix(name, "-"+suffixFor(t, host)) {
		t.Errorf("name %q must keep the hash suffix", name)
	}
}
