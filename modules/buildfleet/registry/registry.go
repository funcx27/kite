// Package registry holds static per-instance registry connection config.
// No runtime protocol probing: buildkitd.toml is daemon-level and cannot
// hot-reload, so config travels with instance creation (DESIGN.md §7.2).
package registry

type Config struct {
	Host     string
	HTTP     bool // plain http
	Insecure bool // https, skip cert verification (self-signed intranet)
	// Mirrors are same-path sources tried before this host when pulling
	// images referenced by Host (buildkitd.toml mirrors semantics).
	Mirrors []string
}

type Table []Config
