package render

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// nameBaseBudget reserves room for "-" plus the 8 hex hash chars inside the
// 63-byte DNS-1123 label limit.
const nameBaseBudget = 63 - 9

// ResourceName derives the shared Ingress/Secret name for a host: the
// sanitized host truncated to the label budget, then "-" plus a hash of the
// normalized host. Hashing the pre-sanitize string keeps hosts that fold to
// the same base (aaa.bb.com vs aaa-bb.com) distinct; when the input holds no
// legal character at all the hash alone is still a valid label.
func ResourceName(host string) string {
	h := normalizeHost(host)
	base := sanitize(h)
	if len(base) > nameBaseBudget {
		base = strings.TrimRight(base[:nameBaseBudget], "-")
	}
	suffix := hashSuffix(h)
	if base == "" {
		return suffix
	}
	return base + "-" + suffix
}

// serviceBaseBudget caps the main-name prefix so the "-s" separator plus the
// 6-char hash of host/app always fits the 63-byte DNS-1123 label limit.
const serviceBaseBudget = 54

// ServiceIngressName derives the per-service object name: the host's main
// name truncated, then "-s" plus a hash of host/app — deterministic, within
// the label limit, and unique per (host, app) in a namespace.
func ServiceIngressName(host, app string) string {
	h := normalizeHost(host)
	base := ResourceName(h)
	if len(base) > serviceBaseBudget {
		base = strings.TrimRight(base[:serviceBaseBudget], "-")
	}
	sum := sha256.Sum256([]byte(h + "/" + app))
	return base + "-s" + hex.EncodeToString(sum[:3])
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

func hashSuffix(normalizedHost string) string {
	sum := sha256.Sum256([]byte(normalizedHost))
	return hex.EncodeToString(sum[:4])
}

// sanitize folds every run of characters outside [a-z0-9] into a single '-'
// and trims leading/trailing dashes. The input must already be lowercased.
func sanitize(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if b.Len() > 0 && !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
