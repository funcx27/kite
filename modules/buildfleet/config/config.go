// Package config loads the buildfleetd YAML configuration. The providers
// list is ordered: provisioning walks it and picks the first provider
// whose capacity fits (preference order).
package config

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/funcx27/kite/modules/buildfleet/registry"
	"github.com/funcx27/kite/modules/buildfleet/resource"
)

type Config struct {
	Addr  string `yaml:"addr"`
	Store string `yaml:"store"`
	// Runtimes maps named runtime profiles; providers reference one by
	// name (empty = "default"). Profiles drive image/mode/limits. After
	// Load returns, access goes through the mu-guarded accessors
	// (DefaultRuntime, SetDefaultRuntime, RuntimeProfiles, Profile): the
	// host rewrites the default profile while serving.
	Runtimes map[string]Runtime `yaml:"runtimes"`
	// Runtime is a compat alias: its fields merge into
	// Runtimes["default"] after loading (single-profile configs stay
	// unchanged).
	Runtime Runtime `yaml:"runtime"`
	// ProviderDefaults holds per-type field defaults (keyed by docker |
	// k8s); providers entries overlay onto them field by field.
	ProviderDefaults map[string]Provider `yaml:"providerDefaults"`
	// DedupeWindow is how long an identical repeat build (same source,
	// dockerfile, args, push target) reuses the previous result instead
	// of rebuilding. Risk: a moved base-image tag within the window is
	// not picked up. Default "0" (concurrency-only dedup); e.g. "5m" enables result reuse.
	DedupeWindow string     `yaml:"dedupeWindow"`
	Providers    []Provider `yaml:"providers"`

	// mu guards Runtimes after load. Profiles are replaced wholesale and
	// never mutated in place, so a snapshot copy is a stable value.
	mu sync.RWMutex
}

type Runtime struct {
	// ImageRef is the buildkitd image, used verbatim for the configured
	// mode. The user is responsible for matching it to Mode (rootless
	// images carry a -rootless-style tag); defaults per mode:
	// moby/buildkit:master (rootful) / moby/buildkit:master-rootless.
	ImageRef string `yaml:"image"`
	// Mode: "rootful" (default) | "rootless".
	Mode string `yaml:"mode"`
	// DefaultResources is the per-instance limit applied when a build
	// request declares no resources. Explicitly declared request
	// resources always win.
	DefaultResources Resources `yaml:"defaultResources"`
	// IdleTTL disposes warm runtimes idle longer than this; empty = 15m,
	// 0 disables reaping.
	IdleTTL string `yaml:"idleTtl"`
	// InsecureRegistries lists registries with non-standard transport,
	// as URLs: "http://host" (plain) or "https://host" (TLS without
	// certificate verification — intranet self-signed). Registries on
	// standard verified https need no entry.
	InsecureRegistries []string `yaml:"insecureRegistries"`
	// Mirrors redirects pulls per upstream host: key is the host a build
	// references (e.g. docker.io), value is the ordered list of sources
	// tried first (e.g. mirror.example.com/dockerhub). Falls back to
	// the upstream itself on failure. Mirror hosts needing http or
	// unverified https also need an insecureRegistries entry.
	Mirrors map[string][]string `yaml:"mirrors"`
	// Hosts adds static /etc/hosts entries to every buildkitd instance
	// (docker --add-host / k8s hostAliases), e.g. intranet git servers.
	Hosts []HostEntry `yaml:"hosts"`
}

// HostEntry maps one IP to one or more hostnames.
type HostEntry struct {
	IP    string   `yaml:"ip"`
	Names []string `yaml:"names"`
}

// Image returns the effective buildkitd image: the configured ref, or
// the per-mode default when empty.
func (r Runtime) Image() string {
	if r.ImageRef != "" {
		return r.ImageRef
	}
	if r.Mode == "rootless" {
		return "moby/buildkit:master-rootless"
	}
	return "moby/buildkit:master"
}

// MustParse parses Resources, falling back to zero on error (validation
// in Load catches bad values earlier).
func (r Resources) MustParse() resource.Resources {
	out, _ := r.Parse()
	return out
}

// IdleTTLParsed returns the runtime idle TTL: empty = 15m, "0" disables.
func (r Runtime) IdleTTLParsed() time.Duration {
	if r.IdleTTL == "" {
		return 15 * time.Minute
	}
	d, err := time.ParseDuration(r.IdleTTL)
	if err != nil {
		return 15 * time.Minute
	}
	return d
}

func (r Runtime) Rootless() bool { return r.Mode == "rootless" }

// Privileged reports whether instances run privileged (rootful).
func (r Runtime) Privileged() bool { return r.Mode != "rootless" }

type Provider struct {
	Type string `yaml:"type"` // docker | k8s
	// Name identifies this provider in logs and Status output; defaults
	// to type, must be unique across the list.
	Name string `yaml:"name"`
	// Runtime selects the runtime profile from runtimes (default:
	// "default").
	Runtime string `yaml:"runtime"`

	// shared
	Total Resources `yaml:"total"`

	// SecurityOpt is docker-only extra hardening.
	SecurityOpt []string `yaml:"securityOpt"`

	// docker: daemon endpoint and mutual-TLS auth.
	// Address forms:
	//   unix:///var/run/docker.sock  local daemon (default when empty)
	//   tcp://<host>:2375            remote daemon. NOTE: remote daemons
	//                                cannot bind-mount the shared
	//                                buildkitd.toml; buildProviders rejects
	//                                them at startup.
	Address string `yaml:"address"`
	// TLS requires address. cert+key are the client pair; ca is optional
	// (system roots when empty); insecureSkipVerify skips server cert
	// verification for self-signed setups.
	TLS *TLS `yaml:"tls"`

	// k8s
	// Cache enables an in-cluster registry (auto-deployed by the daemon)
	// that buildkitd instances use for cross-pod layer cache
	// import/export. Anonymous, cluster-internal only.
	Cache     *Cache `yaml:"cache"`
	Namespace string `yaml:"namespace"`
	// Kubeconfig is an explicit config path; empty = the client-go
	// detection chain (in-cluster ServiceAccount, else KUBECONFIG env /
	// ~/.kube/config).
	Kubeconfig   string `yaml:"kubeconfig"`
	ReadyTimeout string `yaml:"readyTimeout"` // duration string, e.g. 90s
	// PortForward tunnels runtime access through the apiserver (SPDY):
	// for daemons that cannot reach pod networking directly.
	PortForward bool `yaml:"portForward"`
	// PodTemplate is the declarative pod shape (managed-cluster
	// annotations, tolerations, affinity, ...) merged under buildfleet's
	// own pod fields.
	PodTemplate *PodTemplate `yaml:"podTemplate"`
	// ECI translates Alibaba Cloud ECI options into k8s.aliyun.com/eci-*
	// annotations on every runtime pod.
	ECI *ECI `yaml:"eci"`
	// StartupCost is the cold-start penalty recorded on provisioned
	// runtimes; the pool prefers cheaper warm candidates (e.g. 15s for
	// ECI). Duration string, empty = 0.
	StartupCost string `yaml:"startupCost"`
}

// Cache configures the auto-deployed layer-cache registry (k8s only).
type Cache struct {
	Enabled      bool   `yaml:"enabled"`
	Image        string `yaml:"image"`        // default: intranet registry:3 mirror
	StorageClass string `yaml:"storageClass"` // default: cluster default StorageClass
	Size         string `yaml:"size"`         // PVC size, default 20Gi
	// GCSchedule runs registry garbage-collect (retention: the latest
	// cache chain per project) on this cron; default "0 3 * * *", "off"
	// disables the GC job.
	GCSchedule string `yaml:"gcSchedule"`
}

// TLS configures mutual-TLS auth to a remote docker daemon.
type TLS struct {
	CA                 string `yaml:"ca"`
	Cert               string `yaml:"cert"`
	Key                string `yaml:"key"`
	InsecureSkipVerify bool   `yaml:"insecureSkipVerify"`
}

type Resources struct {
	CPU              float64 `yaml:"cpu"`
	Memory           string  `yaml:"memory"`           // quantity, e.g. 16Gi
	EphemeralStorage string  `yaml:"ephemeralStorage"` // optional, k8s only
}

// ReadyTimeoutParsed returns the k8s pod-Ready poll timeout; empty = 60s.
func (p *Provider) ReadyTimeoutParsed() (time.Duration, error) {
	if p.ReadyTimeout == "" {
		return 60 * time.Second, nil
	}
	return time.ParseDuration(p.ReadyTimeout)
}

// StartupCostParsed returns the provisioned-runtime cold-start penalty;
// empty = 0.
func (p *Provider) StartupCostParsed() (time.Duration, error) {
	if p.StartupCost == "" {
		return 0, nil
	}
	return time.ParseDuration(p.StartupCost)
}

// validateDockerEndpoint checks docker address/TLS pairing: TLS needs an
// explicit endpoint, cert and key must come as a pair.
func validateDockerEndpoint(p Provider) error {
	if p.Type != "docker" {
		if p.Address != "" || p.TLS != nil {
			return fmt.Errorf("address/tls are docker-only")
		}
		return nil
	}
	switch {
	case strings.HasPrefix(p.Address, "unix://"), strings.HasPrefix(p.Address, "npipe://"):
		return nil
	case p.Address != "" && !strings.HasPrefix(p.Address, "tcp://"):
		return fmt.Errorf("address must be unix:// or tcp://, got %q", p.Address)
	}
	if p.TLS == nil {
		return nil
	}
	if p.Address == "" {
		return fmt.Errorf("tls requires a remote tcp:// address")
	}
	if (p.TLS.Cert == "") != (p.TLS.Key == "") {
		return fmt.Errorf("tls: cert and key must be set together")
	}
	return nil
}

func (r Resources) Parse() (resource.Resources, error) {
	out := resource.Resources{CPU: r.CPU}
	if r.Memory != "" {
		b, err := resource.ParseQuantity(r.Memory)
		if err != nil {
			return out, fmt.Errorf("memory %q: %w", r.Memory, err)
		}
		out.Memory = b
	}
	if r.EphemeralStorage != "" {
		b, err := resource.ParseQuantity(r.EphemeralStorage)
		if err != nil {
			return out, fmt.Errorf("ephemeralStorage %q: %w", r.EphemeralStorage, err)
		}
		out.EphemeralStorage = b
	}
	return out, nil
}

// RegistryTable builds the registry config: insecure entries from the
// URL list, then mirror-only entries for upstreams without one.
func (r Runtime) RegistryTable() (registry.Table, error) {
	tbl := registry.Table{}
	index := map[string]int{}
	for _, raw := range r.InsecureRegistries {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("config: insecureRegistries: invalid url %q", raw)
		}
		cfg := registry.Config{Host: u.Host}
		switch u.Scheme {
		case "http":
			cfg.HTTP = true
		case "https":
			cfg.Insecure = true // TLS without cert verification
		default:
			return nil, fmt.Errorf("config: insecureRegistries: %q must use http:// or https://", raw)
		}
		if _, dup := index[u.Host]; dup {
			return nil, fmt.Errorf("config: insecureRegistries: duplicate host %q", u.Host)
		}
		index[u.Host] = len(tbl)
		tbl = append(tbl, cfg)
	}
	for upstream, mirrors := range r.Mirrors {
		if _, declared := index[upstream]; declared {
			return nil, fmt.Errorf("config: mirrors[%s]: upstream is also an insecureRegistries entry", upstream)
		}
		for _, m := range mirrors {
			if _, declared := index[m]; declared {
				return nil, fmt.Errorf("config: mirrors[%s]: source %q is itself an insecureRegistries entry", upstream, m)
			}
		}
		tbl = append(tbl, registry.Config{Host: upstream, Mirrors: mirrors})
	}
	return tbl, nil
}

//go:embed default.yaml
var defaultCfg []byte

// Load reads and validates a config file, overlaid on the embedded
// defaults: scalars and maps override per field/key, lists (providers)
// replace wholesale. An empty path loads the defaults alone.
func Load(path string) (*Config, error) {
	if path == "" {
		return parse(nil, "")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	return parse(b, path)
}

// LoadFromBytes parses a config document overlaid on the embedded
// defaults, like Load on the file's contents; empty data loads the
// defaults alone.
func LoadFromBytes(data []byte) (*Config, error) {
	if len(data) == 0 {
		return parse(nil, "")
	}
	return parse(data, "bytes")
}

// parse overlays one YAML document (name labels it in errors; nil skips
// the overlay) on the embedded defaults and validates the merged result.
func parse(overlay []byte, name string) (*Config, error) {
	// KnownFields rejects unknown keys: a typo or a moved field (e.g.
	// provider-level mode) must fail loudly, not silently no-op.
	dec := yaml.NewDecoder(bytes.NewReader(defaultCfg))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("config: parse embedded defaults: %w", err)
	}
	if overlay != nil {
		dec := yaml.NewDecoder(bytes.NewReader(overlay))
		dec.KnownFields(true)
		if err := dec.Decode(&c); err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", name, err)
		}
	}
	// Providers may legitimately be empty for the kite host: its managed
	// providers live in the database and register via the API, so builds
	// fail per-request until one is added. Standalone buildfleetd users
	// configure providers explicitly (buildfleet.example.yaml).
	// Providers overlay onto their type's defaults (embedded
	// providerDefaults), so entries only carry their specifics.
	for i := range c.Providers {
		if d, ok := c.ProviderDefaults[c.Providers[i].Type]; ok {
			merged := d
			mergeProvider(&merged, c.Providers[i])
			merged.Type = c.Providers[i].Type
			c.Providers[i] = merged
		}
	}
	// Merge the compat alias into the default profile: per-field overlay
	// on whatever runtimes.default already carries (embedded presets).
	if c.Runtimes == nil {
		c.Runtimes = map[string]Runtime{}
	}
	def := c.Runtimes["default"]
	mergeRuntime(&def, c.Runtime)
	c.Runtimes["default"] = def
	// Non-default profiles inherit unset fields from default (they
	// typically differ only in mode/image), then keep their overrides.
	for name, r := range c.Runtimes {
		if name == "default" {
			continue
		}
		eff := def
		mergeRuntime(&eff, r)
		c.Runtimes[name] = eff
	}
	if len(c.Runtimes) == 0 {
		return nil, fmt.Errorf("config: no runtime profiles")
	}
	seen := map[string]bool{}
	for i := range c.Providers {
		if c.Providers[i].Name == "" {
			c.Providers[i].Name = c.Providers[i].Type
		}
		if seen[c.Providers[i].Name] {
			return nil, fmt.Errorf("config: duplicate provider name %q", c.Providers[i].Name)
		}
		seen[c.Providers[i].Name] = true
	}
	for i, p := range c.Providers {
		switch p.Type {
		case "docker", "k8s":
		default:
			return nil, fmt.Errorf("config: providers[%d].type must be docker or k8s, got %q", i, p.Type)
		}
		if _, err := p.Total.Parse(); err != nil {
			return nil, fmt.Errorf("config: providers[%d]: %w", i, err)
		}
		if _, err := p.ReadyTimeoutParsed(); err != nil {
			return nil, fmt.Errorf("config: providers[%d].readyTimeout: %w", i, err)
		}
		if err := validateDockerEndpoint(p); err != nil {
			return nil, fmt.Errorf("config: providers[%d]: %w", i, err)
		}
		if p.Type != "k8s" && p.Cache != nil {
			return nil, fmt.Errorf("config: providers[%d]: cache is k8s-only", i)
		}
		if p.Type != "k8s" && (p.PodTemplate != nil || p.ECI != nil) {
			return nil, fmt.Errorf("config: providers[%d]: podTemplate/eci are k8s-only", i)
		}
		if p.ECI != nil {
			if err := p.ECI.validate(); err != nil {
				return nil, fmt.Errorf("config: providers[%d].eci: %w", i, err)
			}
		}
		if _, err := p.StartupCostParsed(); err != nil {
			return nil, fmt.Errorf("config: providers[%d].startupCost: %w", i, err)
		}
		if p.Cache != nil && p.Cache.Enabled && p.Cache.Size != "" {
			if _, err := resource.ParseQuantity(p.Cache.Size); err != nil {
				return nil, fmt.Errorf("config: providers[%d].cache.size: %w", i, err)
			}
		}
		if p.Runtime == "" {
			p.Runtime = "default"
		}
		if _, ok := c.Runtimes[p.Runtime]; !ok {
			return nil, fmt.Errorf("config: providers[%d].runtime: unknown profile %q", i, p.Runtime)
		}
		c.Providers[i] = p
	}
	for name, r := range c.Runtimes {
		if err := validateRuntime(name, r); err != nil {
			return nil, err
		}
	}
	if _, err := time.ParseDuration(c.DedupeWindow); err != nil {
		return nil, fmt.Errorf("config: dedupeWindow: %w", err)
	}

	return &c, nil
}

// mergeRuntime overlays non-zero fields of src onto dst.
func mergeRuntime(dst *Runtime, src Runtime) {
	if src.ImageRef != "" {
		dst.ImageRef = src.ImageRef
	}
	if src.Mode != "" {
		dst.Mode = src.Mode
	}
	if src.IdleTTL != "" {
		dst.IdleTTL = src.IdleTTL
	}
	if src.DefaultResources.CPU != 0 {
		dst.DefaultResources.CPU = src.DefaultResources.CPU
	}
	if src.DefaultResources.Memory != "" {
		dst.DefaultResources.Memory = src.DefaultResources.Memory
	}
	if src.DefaultResources.EphemeralStorage != "" {
		dst.DefaultResources.EphemeralStorage = src.DefaultResources.EphemeralStorage
	}
	if len(src.InsecureRegistries) > 0 {
		dst.InsecureRegistries = src.InsecureRegistries
	}
	if len(src.Mirrors) > 0 {
		dst.Mirrors = src.Mirrors
	}
	if len(src.Hosts) > 0 {
		dst.Hosts = src.Hosts
	}
}

// mergeProvider overlays non-zero fields of src onto dst (provider
// entries over their type defaults).
func mergeProvider(dst *Provider, src Provider) {
	if src.Name != "" {
		dst.Name = src.Name
	}
	if src.Runtime != "" {
		dst.Runtime = src.Runtime
	}
	if src.Total.CPU != 0 {
		dst.Total.CPU = src.Total.CPU
	}
	if src.Total.Memory != "" {
		dst.Total.Memory = src.Total.Memory
	}
	if src.Total.EphemeralStorage != "" {
		dst.Total.EphemeralStorage = src.Total.EphemeralStorage
	}
	if len(src.SecurityOpt) > 0 {
		dst.SecurityOpt = src.SecurityOpt
	}
	if src.Address != "" {
		dst.Address = src.Address
	}
	if src.TLS != nil {
		dst.TLS = src.TLS
	}
	if src.Cache != nil {
		if dst.Cache == nil {
			dst.Cache = src.Cache
		} else {
			mergeCache(dst.Cache, src.Cache)
		}
	}
	if src.Namespace != "" {
		dst.Namespace = src.Namespace
	}
	if src.Kubeconfig != "" {
		dst.Kubeconfig = src.Kubeconfig
	}
	if src.ReadyTimeout != "" {
		dst.ReadyTimeout = src.ReadyTimeout
	}
	if src.PortForward {
		dst.PortForward = true
	}
	if src.PodTemplate != nil {
		dst.PodTemplate = src.PodTemplate
	}
	if src.ECI != nil {
		dst.ECI = src.ECI
	}
	if src.StartupCost != "" {
		dst.StartupCost = src.StartupCost
	}
}

func mergeCache(dst, src *Cache) {
	if src.Image != "" {
		dst.Image = src.Image
	}
	if src.StorageClass != "" {
		dst.StorageClass = src.StorageClass
	}
	if src.Size != "" {
		dst.Size = src.Size
	}
	if src.GCSchedule != "" {
		dst.GCSchedule = src.GCSchedule
	}
	if src.Enabled {
		dst.Enabled = true
	}
}

// validateRuntime checks one profile.
func validateRuntime(name string, r Runtime) error {
	if _, err := r.RegistryTable(); err != nil {
		return fmt.Errorf("config: runtimes[%s]: %w", name, err)
	}
	if err := validateHosts(r.Hosts); err != nil {
		return fmt.Errorf("config: runtimes[%s]: %w", name, err)
	}
	if _, err := r.DefaultResources.Parse(); err != nil {
		return fmt.Errorf("config: runtimes[%s].defaultResources: %w", name, err)
	}
	switch r.Mode {
	case "", "rootful", "rootless":
	default:
		return fmt.Errorf("config: runtimes[%s].mode must be rootful or rootless, got %q", name, r.Mode)
	}
	return nil
}

// DefaultRuntime returns the default profile snapshot.
func (c *Config) DefaultRuntime() Runtime {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Runtimes["default"]
}

// SetDefaultRuntime replaces the default profile; the serving-time write
// path (runtime settings) goes through it so concurrent profile reads
// stay race-free.
func (c *Config) SetDefaultRuntime(r Runtime) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Runtimes["default"] = r
}

// SetRuntimeImages overlays the two build images onto the runtime profiles:
// "default" (rootful) and "rootless". An empty ref resets the profile to its
// per-mode built-in default; a profile absent from the config is skipped.
// Profiles are replaced wholesale like SetDefaultRuntime.
func (c *Config) SetRuntimeImages(rootfulRef, rootlessRef string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.Runtimes["default"]; ok {
		r.ImageRef = rootfulRef
		c.Runtimes["default"] = r
	}
	if r, ok := c.Runtimes["rootless"]; ok {
		r.ImageRef = rootlessRef
		c.Runtimes["rootless"] = r
	}
}

// RuntimeProfiles returns a copy of the full profile table.
func (c *Config) RuntimeProfiles() map[string]Runtime {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]Runtime, len(c.Runtimes))
	for name, r := range c.Runtimes {
		out[name] = r
	}
	return out
}

// Profile returns the provider's runtime profile (validated at load).
func (c *Config) Profile(p Provider) Runtime {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.Runtimes[p.Runtime]
	if !ok {
		r = c.Runtimes["default"]
	}
	return r
}

// Gen returns a stable hash identifying the runtime-affecting
// configuration (mode, image, rendered registry table, hosts). Changing
// any of these invalidates existing instances; daemon-side-only fields
// (defaultResources, idleTtl, total, addr, store) are excluded.
func (r Runtime) Gen(registries registry.Table) string {
	toml, err := registry.RenderToml(registries)
	if err != nil {
		toml = "render-error"
	}
	var hosts []string
	for _, h := range r.Hosts {
		hosts = append(hosts, h.IP+"="+strings.Join(h.Names, ","))
	}
	h := sha256.Sum256([]byte(r.Mode + "\x00" + r.Image() + "\x00" + toml + "\x00" + strings.Join(hosts, "\x00")))
	return hex.EncodeToString(h[:8])
}

// validateHosts rejects malformed /etc/hosts entries early.
func validateHosts(entries []HostEntry) error {
	for i, h := range entries {
		if net.ParseIP(h.IP) == nil {
			return fmt.Errorf("config: runtime.hosts[%d]: invalid ip %q", i, h.IP)
		}
		if len(h.Names) == 0 {
			return fmt.Errorf("config: runtime.hosts[%d]: at least one hostname is required", i)
		}
	}
	return nil
}
