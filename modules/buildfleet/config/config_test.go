package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadMixedProviders(t *testing.T) {
	p := writeConfig(t, `
addr: 127.0.0.1:8484
store: /tmp/store
runtime:
  mode: rootless
  image: mirror.example.com/buildkit:master-rootless
  defaultResources: {cpu: 4, memory: 8Gi}
  idleTtl: 30m
  hosts:
    - {ip: 10.0.0.5, names: [git.internal.example.com]}
  insecureRegistries:
    - http://registry.local:5000
    - https://mirror.example.com
  mirrors:
    docker.io:
      - mirror.example.com/dockerhub
    registry.k8s.io:
      - mirror.example.com/k8s
providers:
  - type: k8s
    namespace: buildkit
    kubeconfig: ~/.kube/config
    readyTimeout: 90s
    total: {cpu: 32, memory: 64Gi}
  - type: docker
    total: {cpu: 8, memory: 16Gi}
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:8484" || c.Store != "/tmp/store" {
		t.Fatalf("addr/store: %q/%q", c.Addr, c.Store)
	}
	if len(c.Providers) != 2 || c.Providers[0].Type != "k8s" || c.Providers[1].Type != "docker" {
		t.Fatalf("provider order lost: %+v", c.Providers)
	}
	if d, _ := c.Providers[0].ReadyTimeoutParsed(); d != 90*time.Second {
		t.Fatalf("readyTimeout: %v", d)
	}
	r, err := c.Providers[0].Total.Parse()
	if err != nil || r.CPU != 32 || r.Memory != 64*1024*1024*1024 {
		t.Fatalf("k8s total: %+v err=%v", r, err)
	}
	if c.Runtime.Mode != "rootless" {
		t.Fatalf("runtime mode: %q", c.Runtime.Mode)
	}
	if got := c.Runtime.Image(); got != "mirror.example.com/buildkit:master-rootless" {
		t.Fatalf("rootless image: %q", got)
	}
	if !c.Runtime.Rootless() || c.Runtime.Privileged() {
		t.Fatal("rootless mode flags wrong")
	}
	if rr := c.Runtime.DefaultResources.MustParse(); rr.CPU != 4 || rr.Memory != 8*1024*1024*1024 {
		t.Fatalf("runtime resources: %+v", rr)
	}
	if d := c.Runtime.IdleTTLParsed(); d != 30*time.Minute {
		t.Fatalf("idle ttl: %v", d)
	}
	tbl, err := c.Runtime.RegistryTable()
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl) != 4 {
		t.Fatalf("registry table: %+v", tbl)
	}
	byHost := map[string]int{}
	for i, rc := range tbl {
		byHost[rc.Host] = i
	}
	if !tbl[byHost["registry.local:5000"]].HTTP {
		t.Fatal("http scheme lost")
	}
	if !tbl[byHost["mirror.example.com"]].Insecure {
		t.Fatal("https (unverified) scheme lost")
	}
	dockerIO := tbl[byHost["docker.io"]]
	if len(dockerIO.Mirrors) != 1 || dockerIO.Mirrors[0] != "mirror.example.com/dockerhub" {
		t.Fatalf("docker.io mirrors lost: %+v", dockerIO)
	}
	k8s := tbl[byHost["registry.k8s.io"]]
	if len(k8s.Mirrors) != 1 || k8s.Mirrors[0] != "mirror.example.com/k8s" {
		t.Fatalf("registry.k8s.io mirrors lost: %+v", k8s)
	}

	if len(c.Runtime.Hosts) != 1 || c.Runtime.Hosts[0].IP != "10.0.0.5" || c.Runtime.Hosts[0].Names[0] != "git.internal.example.com" {
		t.Fatalf("runtime hosts lost: %+v", c.Runtime.Hosts)
	}
}

func TestLoadDefaultsAndValidation(t *testing.T) {
	c, err := Load(writeConfig(t, "providers:\n  - type: docker\n"))
	if err != nil {
		t.Fatal(err)
	}
	// Embedded defaults apply underneath: addr comes from default.yaml.
	if c.Addr != "127.0.0.1:8484" || c.Runtime.Image() != "moby/buildkit:master" {
		t.Fatalf("defaults: %+v", c)
	}
	if d := c.Runtime.IdleTTLParsed(); d != 15*time.Minute {
		t.Fatalf("idle ttl default: %v", d)
	}
	if got := c.Runtime.Image(); got != "moby/buildkit:master" {
		t.Fatalf("default image: %q", got)
	}

	if _, err := Load(writeConfig(t, "")); err == nil {
		t.Fatal("no providers must fail")
	}
	if _, err := Load(writeConfig(t, "providers:\n  - type: vm\n")); err == nil {
		t.Fatal("unknown type must fail")
	}
	if _, err := Load(writeConfig(t, "providers:\n  - type: k8s\n    mode: bogus\n")); err == nil {
		t.Fatal("unknown mode must fail")
	}
	if _, err := Load(writeConfig(t, "providers:\n  - type: docker\n    readyTimeout: xx\n")); err == nil {
		t.Fatal("bad duration must fail")
	}
	bad := `
providers:
  - type: docker
runtime:
  insecureRegistries:
    - ftp://a.example.com
`
	if _, err := Load(writeConfig(t, bad)); err == nil {
		t.Fatal("non-http(s) url must fail")
	}
	nourl := `
providers:
  - type: docker
runtime:
  insecureRegistries:
    - "a.example.com"
`
	if _, err := Load(writeConfig(t, nourl)); err == nil {
		t.Fatal("bare host without scheme must fail")
	}
	// Mirror source that is itself an insecureRegistries entry would
	// recurse (pulling the cache via the cache).
	rec := `
providers:
  - type: docker
runtime:
  insecureRegistries:
    - http://cache.example.com
  mirrors:
    upstream.example.com:
      - cache.example.com
`
	if _, err := Load(writeConfig(t, rec)); err == nil {
		t.Fatal("mirror source duplicating an insecure entry must fail")
	}
	badHost := `
providers:
  - type: docker
runtime:
  hosts:
    - {ip: "not-an-ip", names: [a.example.com]}
`
	if _, err := Load(writeConfig(t, badHost)); err == nil {
		t.Fatal("invalid hosts ip must fail")
	}
	namelessHost := `
providers:
  - type: docker
runtime:
  hosts:
    - {ip: 10.0.0.5, names: []}
`
	if _, err := Load(writeConfig(t, namelessHost)); err == nil {
		t.Fatal("hosts entry without names must fail")
	}
	tlsNoAddr := `
providers:
  - type: docker
    tls: {cert: /c.pem, key: /k.pem}
`
	if _, err := Load(writeConfig(t, tlsNoAddr)); err == nil {
		t.Fatal("tls without address must fail")
	}
	certOnly := `
providers:
  - type: docker
    address: tcp://10.0.0.9:2375
    tls: {cert: /c.pem}
`
	if _, err := Load(writeConfig(t, certOnly)); err == nil {
		t.Fatal("tls cert without key must fail")
	}
	badScheme := `
providers:
  - type: docker
    address: https://192.168.1.10:2376
`
	if _, err := Load(writeConfig(t, badScheme)); err == nil {
		t.Fatal("non unix/tcp address scheme must fail")
	}
	k8sAddr := `
providers:
  - type: k8s
    kubeconfig: ~/.kube/config
    address: tcp://10.0.0.9:2375
`
	if _, err := Load(writeConfig(t, k8sAddr)); err == nil {
		t.Fatal("address on k8s provider must fail")
	}
}

func TestK8sCacheConfig(t *testing.T) {
	ok := `
providers:
  - type: k8s
    kubeconfig: ~/.kube/config
    cache:
      enabled: true
      image: registry.local/registry:3
      storageClass: fast
      size: 50Gi
`
	c, err := Load(writeConfig(t, ok))
	if err != nil {
		t.Fatal(err)
	}
	ch := c.Providers[0].Cache
	if !ch.Enabled || ch.Image != "registry.local/registry:3" || ch.StorageClass != "fast" || ch.Size != "50Gi" {
		t.Fatalf("cache config lost: %+v", ch)
	}

	dockerCache := `
providers:
  - type: docker
    cache: {enabled: true}
`
	if _, err := Load(writeConfig(t, dockerCache)); err == nil {
		t.Fatal("cache on docker provider must fail")
	}
	badSize := `
providers:
  - type: k8s
    kubeconfig: ~/.kube/config
    cache: {enabled: true, size: bogus}
`
	if _, err := Load(writeConfig(t, badSize)); err == nil {
		t.Fatal("bad cache size must fail")
	}
}

func TestDedupeWindowDefault(t *testing.T) {
	c, err := Load(writeConfig(t, "providers:\n  - type: docker\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.DedupeWindow != "0" {
		t.Fatalf("dedupeWindow default = %q, want 0 (concurrency-only)", c.DedupeWindow)
	}
}

func TestOverlayOnEmbeddedDefaults(t *testing.T) {
	// User file overrides scalars and replaces the providers list;
	// untouched embedded fields survive.
	c, err := Load(writeConfig(t, "providers:\n  - type: k8s\n    kubeconfig: ~/.kube/config\n    cache: {enabled: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 1 || c.Providers[0].Type != "k8s" {
		t.Fatalf("providers must replace wholesale: %+v", c.Providers)
	}
	if c.Addr != "127.0.0.1:8484" || c.Store != "/tmp/buildfleet-store" {
		t.Fatalf("embedded defaults lost: %+v", c)
	}
	if c.Runtimes["default"].IdleTTL != "15m" {
		t.Fatalf("embedded idleTtl lost: %q", c.Runtimes["default"].IdleTTL)
	}

	// No path at all: embedded defaults alone must validate.
	c2, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(c2.Providers) != 1 || c2.Providers[0].Type != "docker" {
		t.Fatalf("embedded default providers: %+v", c2.Providers)
	}
	for _, name := range []string{"default", "rootful", "rootless"} {
		if _, ok := c2.Runtimes[name]; !ok {
			t.Fatalf("embedded preset %q missing", name)
		}
	}
	if c2.Runtimes["rootless"].Mode != "rootless" {
		t.Fatalf("rootless preset mode: %+v", c2.Runtimes["rootless"])
	}
}

func TestRuntimeProfiles(t *testing.T) {
	cfg := `
runtimes:
  rootless-cluster:
    mode: rootless
    image: registry.local/buildkit:master-rootless
providers:
  - type: k8s
    kubeconfig: ~/.kube/config
    runtime: rootless-cluster
  - type: docker
    total: {cpu: 8, memory: 16Gi}
`
	c, err := Load(writeConfig(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers[0].Runtime != "rootless-cluster" {
		t.Fatalf("profile ref: %q", c.Providers[0].Runtime)
	}
	if got := c.Profile(c.Providers[0]); got.Mode != "rootless" || got.Image() != "registry.local/buildkit:master-rootless" {
		t.Fatalf("profile lookup: %+v", got)
	}
	// Unspecified provider falls back to default; compat runtime: alias merges in.
	if c.Providers[1].Runtime != "default" {
		t.Fatalf("default ref: %q", c.Providers[1].Runtime)
	}

	badRef := `
runtimes:
  default: {mode: rootful}
providers:
  - type: docker
    runtime: nope
`
	if _, err := Load(writeConfig(t, badRef)); err == nil {
		t.Fatal("unknown profile reference must fail")
	}

	alias := `
runtime:
  mode: rootless
  image: registry.local/buildkit:master-rootless
providers:
  - type: docker
`
	c2, err := Load(writeConfig(t, alias))
	if err != nil {
		t.Fatal(err)
	}
	def := c2.Runtimes["default"]
	if def.Mode != "rootless" || def.ImageRef != "registry.local/buildkit:master-rootless" {
		t.Fatalf("runtime: alias not merged into default profile: %+v", def)
	}
}

func TestProfileInheritsDefault(t *testing.T) {
	cfg := `
runtimes:
  default:
    mode: rootful
    image: registry.local/buildkit:master
    idleTtl: 30m
    defaultResources: {cpu: 4, memory: 8Gi, ephemeralStorage: 40Gi}
  slim:
    mode: rootless
    image: registry.local/buildkit:master-rootless
providers:
  - type: docker
    runtime: slim
`
	c, err := Load(writeConfig(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	slim := c.Runtimes["slim"]
	if slim.Mode != "rootless" || slim.ImageRef != "registry.local/buildkit:master-rootless" {
		t.Fatalf("own fields lost: %+v", slim)
	}
	// Inherited from default.
	if slim.IdleTTL != "30m" || slim.DefaultResources.CPU != 4 || slim.DefaultResources.EphemeralStorage != "40Gi" {
		t.Fatalf("default inheritance lost: %+v", slim)
	}

	// Embedded presets: rootless inherits idleTtl/resources from default.
	c2, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	rl := c2.Runtimes["rootless"]
	if rl.IdleTTL != "15m" || rl.DefaultResources.Memory != "4Gi" || rl.Mode != "rootless" {
		t.Fatalf("preset inheritance: %+v", rl)
	}
}

func TestProviderDefaultsMerge(t *testing.T) {
	// Embedded providerDefaults: k8s defaults to the rootless profile and
	// carries cache sub-defaults; entries only add their specifics.
	c, err := Load(writeConfig(t, `
providers:
  - type: k8s
    kubeconfig: ~/.kube/config
    cache: {enabled: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	p := c.Providers[0]
	if p.Runtime != "rootless" {
		t.Fatalf("k8s default profile = %q, want rootless", p.Runtime)
	}
	if p.Cache == nil || !p.Cache.Enabled || p.Cache.Size != "20Gi" || p.Cache.Image == "" {
		t.Fatalf("cache defaults not inherited: %+v", p.Cache)
	}
	// Explicit fields win.
	c2, err := Load(writeConfig(t, `
providers:
  - type: k8s
    kubeconfig: ~/.kube/config
    runtime: default
    readyTimeout: 90s
`))
	if err != nil {
		t.Fatal(err)
	}
	p2 := c2.Providers[0]
	if p2.Runtime != "default" || p2.ReadyTimeout != "90s" {
		t.Fatalf("explicit override lost: %+v", p2)
	}
	if p2.Cache != nil && p2.Cache.Enabled {
		t.Fatal("cache enabled must stay per-provider")
	}
}
