// Package creds implements the unified credential table. Credentials are
// resolved caller-side and travel per-Solve over the buildkit session;
// runtime instances hold none (DESIGN.md §7.1).
package creds

import (
	"encoding/base64"
	"errors"
	"os"
	"strings"
)

type Kind string

const (
	Registry Kind = "registry"
	Git      Kind = "git"
)

type AuthRef struct {
	Env string // environment variable holding "username:password"
}

type Credential struct {
	Kind  Kind
	Match string // registry host[/namespace] or git host; longest prefix wins
	Auth  AuthRef
	// Direct holds a literal secret (API passthrough from the caller);
	// it bypasses the env lookup and must never be logged.
	Direct *Secret
}

type Table []Credential

// Match returns the credential with the longest matching prefix, or nil
// (anonymous) when nothing matches.
func (t Table) Match(kind Kind, host string) *Credential {
	var best *Credential
	for i := range t {
		c := &t[i]
		if c.Kind != kind {
			continue
		}
		if !strings.HasPrefix(host, c.Match) {
			continue
		}
		if best == nil || len(c.Match) > len(best.Match) {
			best = c
		}
	}
	return best
}

type Secret struct {
	Username string
	Secret   string
}

// Resolve returns the direct secret when set, else reads the env var.
// The returned secret is only used to build the session auth provider; it
// is never logged or sent with the request body.
func (c *Credential) Resolve() (Secret, error) {
	if c.Direct != nil {
		return *c.Direct, nil
	}
	v, ok := os.LookupEnv(c.Auth.Env)
	if !ok {
		return Secret{}, errors.New("creds: env " + c.Auth.Env + " not set")
	}
	user, pass, ok := strings.Cut(v, ":")
	if !ok {
		// tolerate raw base64 basic auth ("user:pass" encoded)
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return Secret{}, errors.New("creds: env " + c.Auth.Env + " malformed")
		}
		user, pass, ok = strings.Cut(string(raw), ":")
		if !ok {
			return Secret{}, errors.New("creds: env " + c.Auth.Env + " malformed")
		}
	}
	return Secret{Username: user, Secret: pass}, nil
}
