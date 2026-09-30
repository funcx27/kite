package builder

import (
	"context"
	"fmt"
	"net"

	dockerclitypes "github.com/docker/cli/cli/config/types"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/moby/buildkit/session/secrets/secretsprovider"

	"github.com/funcx27/kite/modules/buildfleet/creds"
)

// AllowedHosts returns a predicate reporting whether host has a configured
// registry credential. Auth is only ever served for allowed hosts: no creds
// leak to unconfigured registries (DESIGN.md §7.1).
func AllowedHosts(tbl creds.Table) func(string) bool {
	return func(host string) bool {
		return tbl.Match(creds.Registry, host) != nil
	}
}

// FindAuth resolves the credential for host (longest-prefix match).
func FindAuth(tbl creds.Table, host string) (dockerclitypes.AuthConfig, error) {
	c := tbl.Match(creds.Registry, host)
	if c == nil {
		return dockerclitypes.AuthConfig{}, fmt.Errorf("builder: no credential for %s", host)
	}
	sec, err := c.Resolve()
	if err != nil {
		return dockerclitypes.AuthConfig{}, fmt.Errorf("builder: resolve %s: %w", host, err)
	}
	return dockerclitypes.AuthConfig{Username: sec.Username, Password: sec.Secret}, nil
}

// newCredsSession builds a session attachable that answers buildkit's auth
// callbacks from the credential table. Credentials live only inside this
// per-Solve session; the runtime instance holds none (DESIGN.md §7.1).
// Hosts with no table entry must return empty auth (anonymous pull), not
// an error: buildkit queries auth for every registry.
func credsSession(in SolveInput) []session.Attachable {
	var out []session.Attachable
	if sp, err := newCredsSession(in.Creds); err == nil && sp != nil {
		out = append(out, sp)
	}
	if sp, err := newSourceSecretsSession(in.GitSecrets, in.HTTPSecrets); err == nil && sp != nil {
		out = append(out, sp)
	}
	return out
}

// newSourceSecretsSession exposes remote-source credentials (git and http)
// to buildkitd over the secrets session; it queries host-scoped names at
// fetch time (sourceSecretStore).
func newSourceSecretsSession(git, http map[string]SourceSecret) (session.Attachable, error) {
	store := sourceSecretStore(git, http)
	if len(store) == 0 {
		return nil, nil
	}
	return secretsprovider.FromMap(store), nil
}

// sourceSecretStore flattens per-host source credentials into the
// secret-name -> value map the session serves. Name formats are fixed by
// buildkitd's source handlers: git queries GIT_AUTH_HEADER.<host> /
// GIT_AUTH_TOKEN.<host> (host includes the port), the http handler queries
// HTTP_AUTH_HEADER_<hostname> / HTTP_AUTH_TOKEN_<hostname> (hostname only,
// port stripped; the token variant is wrapped as Bearer by buildkitd).
func sourceSecretStore(git, http map[string]SourceSecret) map[string][]byte {
	store := map[string][]byte{}
	for host, s := range git {
		switch {
		case s.Header != "":
			store["GIT_AUTH_HEADER."+host] = []byte(s.Header)
		case s.Token != "":
			store["GIT_AUTH_TOKEN."+host] = []byte(s.Token)
		}
	}
	for host, s := range http {
		name := hostName(host)
		switch {
		case s.Header != "":
			store["HTTP_AUTH_HEADER_"+name] = []byte(s.Header)
		case s.Token != "":
			store["HTTP_AUTH_TOKEN_"+name] = []byte(s.Token)
		}
	}
	return store
}

// hostName strips the port from a host[:port] key, mirroring the
// u.Hostname() form buildkitd's http source matches secrets by.
func hostName(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func newCredsSession(tbl creds.Table) (session.Attachable, error) {
	if len(tbl) == 0 {
		return nil, nil
	}
	provider := func(ctx context.Context, host string, scope []string, _ authprovider.ExpireCachedAuthCheck) (dockerclitypes.AuthConfig, error) {
		c, err := FindAuth(tbl, host)
		if err != nil {
			// No table entry for this host: anonymous pull.
			return dockerclitypes.AuthConfig{}, nil
		}
		return c, nil
	}
	return authprovider.NewDockerAuthProvider(authprovider.DockerAuthProviderConfig{
		AuthConfigProvider: provider,
	}), nil
}
