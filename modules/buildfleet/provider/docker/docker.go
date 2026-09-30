// Package docker supplies buildkitd runtimes as local docker containers
// (DESIGN.md §4.2: container path is the default; the daemon's built-in
// buildkit is a single-instance zero-startup fallback, not the default).
package docker

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	dclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	bkclient "github.com/moby/buildkit/client"

	"github.com/funcx27/kite/modules/buildfleet/registry"
	"github.com/funcx27/kite/modules/buildfleet/resource"
	"github.com/funcx27/kite/modules/buildfleet/runtime"
)

const buildkitPort = "1234/tcp"

type Config struct {
	Total      resource.Resources // node capacity minus system reserve
	Image      string             // global BuildkitImage (DESIGN.md §5.2)
	Registries registry.Table
	// Address is the docker daemon endpoint (unix:///var/run/docker.sock
	// or tcp://host:2375). Empty means the environment default (FromEnv).
	Address string
	// TLS configures mutual-TLS daemon auth for remote endpoints; nil
	// means plain http/https system defaults.
	TLS *TLSConfig
	// DockerAPI overrides the docker client (nil means build one from
	// Address/TLS; capacity accounting still works for pre-flight
	// scheduling).
	DockerAPI dclient.APIClient
	// Privileged grants the buildkitd container CAP_SYS_ADMIN for its
	// snapshotter mounts; required for rootful images (buildx does the
	// same). Rootless images need seccomp=unconfined instead.
	Privileged bool
	// TomlPath is the shared buildkitd.toml all containers bind-mount.
	// Written once by the daemon at startup; never per-instance.
	TomlPath string
	// ConfigGen identifies the effective runtime configuration (image,
	// mode, rendered buildkitd.toml). Reconcile only adopts containers
	// whose gen label matches, so config changes shed stale instances.
	ConfigGen string
	// ExtraHosts adds static /etc/hosts entries ("name:ip") to every
	// container (intranet git servers and similar).
	ExtraHosts  []string
	SecurityOpt []string
}

// TLSConfig holds mutual-TLS credentials for a remote docker daemon.
type TLSConfig struct {
	CA                 string // PEM; empty = system roots
	Cert               string // client cert PEM (pair with Key)
	Key                string // client key PEM
	InsecureSkipVerify bool   // skip server cert verification (self-signed setups)
}

// NewClient builds a docker API client for the given endpoint. address may
// be empty to inherit DOCKER_HOST / local socket defaults.
func NewClient(address string, tc *TLSConfig) (dclient.APIClient, error) {
	var opts []dclient.Opt
	if address != "" {
		opts = append(opts, dclient.WithHost(address))
	}
	if tc != nil {
		tlsc := &tls.Config{InsecureSkipVerify: tc.InsecureSkipVerify} //nolint:gosec // opt-in via config
		if tc.CA != "" {
			pem, err := os.ReadFile(tc.CA)
			if err != nil {
				return nil, fmt.Errorf("docker client: read CA %s: %w", tc.CA, err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("docker client: no certs in CA %s", tc.CA)
			}
			tlsc.RootCAs = pool
		}
		if tc.Cert != "" || tc.Key != "" {
			pair, err := tls.LoadX509KeyPair(tc.Cert, tc.Key)
			if err != nil {
				return nil, fmt.Errorf("docker client: load client keypair: %w", err)
			}
			tlsc.Certificates = []tls.Certificate{pair}
		}
		opts = append(opts, dclient.WithHTTPClient(&http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsc},
		}))
	}
	opts = append(opts, dclient.WithAPIVersionNegotiation())
	return dclient.NewClientWithOpts(opts...)
}

// endpointClass derives how published ports are reached: local daemons
// (unix/npipe socket) bind loopback only, remote daemons (tcp) must
// publish on all interfaces and are addressed via the daemon host itself.
// Only schemes the docker SDK accepts (unix, npipe, tcp) are allowed.
func endpointClass(address string) (hostIP, addrHost string, err error) {
	if address == "" || strings.HasPrefix(address, "unix://") || strings.HasPrefix(address, "npipe://") {
		return "127.0.0.1", "127.0.0.1", nil
	}
	u, err := url.Parse(address)
	if err != nil {
		return "", "", fmt.Errorf("docker endpoint %q: %w", address, err)
	}
	if u.Scheme != "tcp" {
		return "", "", fmt.Errorf("docker endpoint %q: must be unix:// or tcp://", address)
	}
	host := u.Hostname()
	if host == "" {
		return "", "", fmt.Errorf("docker endpoint %q: missing host", address)
	}
	return "", host, nil
}

type Provider struct {
	cfg        Config
	cli        dclient.APIClient
	hostIP     string // HostIP for published ports: loopback (local) or "" = 0.0.0.0 (remote)
	addrHost   string // host part of runtime Addrs
	mu         sync.Mutex
	live       resource.Resources
	containers map[string]containerRec // container id -> record
}

type containerRec struct {
	resources resource.Resources
}

func New(cfg Config) (*Provider, error) {
	cli := cfg.DockerAPI
	if cli == nil {
		var err error
		if cli, err = NewClient(cfg.Address, cfg.TLS); err != nil {
			return nil, err
		}
	}
	endpoint := cfg.Address
	if endpoint == "" {
		endpoint = cli.DaemonHost()
	}
	hostIP, addrHost, err := endpointClass(endpoint)
	if err != nil {
		return nil, err
	}
	// The toml bind-mount source is resolved on the daemon's host, not
	// ours: with a remote daemon it would point at a nonexistent file.
	if hostIP == "" && cfg.TomlPath != "" {
		return nil, fmt.Errorf("docker endpoint %q: remote daemons cannot bind-mount the local buildkitd.toml (%s); use a local daemon or a k8s provider", endpoint, cfg.TomlPath)
	}
	return &Provider{cfg: cfg, cli: cli, hostIP: hostIP, addrHost: addrHost, containers: map[string]containerRec{}}, nil
}

// addr joins the runtime address for a published host port.
func (p *Provider) addr(port string) string {
	return "tcp://" + net.JoinHostPort(p.addrHost, port)
}

func (p *Provider) Capacity() resource.Resources {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.Total.Sub(p.live)
}

func (p *Provider) trackLive(r resource.Resources) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.live = p.live.Add(r)
}

func (p *Provider) untrackLive(r resource.Resources) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.live = p.live.Sub(r)
}

// Provision starts a restricted buildkitd container with the static registry
// config bind-mounted read-only, and waits for its host port assignment.
func (p *Provider) Provision(ctx context.Context, spec runtime.Spec) (*runtime.Runtime, error) {
	if err := p.ensureImage(ctx, p.cfg.Image); err != nil {
		return nil, err
	}
	cid, err := p.createContainer(ctx, spec)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.containers[cid] = containerRec{resources: spec.Resources}
	p.mu.Unlock()
	p.trackLive(spec.Resources)

	if err := p.cli.ContainerStart(ctx, cid, container.StartOptions{}); err != nil {
		_ = p.Dispose(context.WithoutCancel(ctx), cid)
		return nil, fmt.Errorf("docker provision: start: %w", err)
	}
	port, err := p.waitReady(ctx, cid, 60*time.Second)
	if err != nil {
		_ = p.Dispose(context.WithoutCancel(ctx), cid)
		return nil, err
	}
	return &runtime.Runtime{ID: cid, Spec: spec, Addr: p.addr(port), State: runtime.Warm}, nil
}

// ensureImage pulls the buildkitd image when missing locally; the daemon's
// own registry mirrors apply. ImageCreate never auto-pulls on ContainerCreate.
func (p *Provider) ensureImage(ctx context.Context, ref string) error {
	if _, err := p.cli.ImageInspect(ctx, ref); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("docker provision: inspect image: %w", err)
	}
	rc, err := p.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("docker provision: pull image %s: %w", ref, err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("docker provision: pull image %s: %w", ref, err)
	}
	return nil
}

func (p *Provider) createContainer(ctx context.Context, spec runtime.Spec) (string, error) {
	hc := &container.HostConfig{
		Resources: container.Resources{
			NanoCPUs: int64(spec.Resources.CPU * 1e9),
			Memory:   int64(spec.Resources.Memory),
		},
		Privileged:   p.cfg.Privileged,
		SecurityOpt:  p.cfg.SecurityOpt,
		ExtraHosts:   p.cfg.ExtraHosts,
		PortBindings: nat.PortMap{buildkitPort: []nat.PortBinding{{HostIP: p.hostIP}}},
		Mounts: []mount.Mount{{
			Type:     mount.TypeBind,
			Source:   p.cfg.TomlPath,
			Target:   "/etc/buildkit/buildkitd.toml",
			ReadOnly: true,
		}},
	}
	cfg := &container.Config{
		Image:        p.cfg.Image,
		Cmd:          []string{"buildkitd", "--addr", "tcp://0.0.0.0:1234"},
		ExposedPorts: nat.PortSet{buildkitPort: struct{}{}},
		Labels: map[string]string{
			"buildfleet":            "runtime",
			"buildfleet/config-gen": p.cfg.ConfigGen,
		},
	}
	// The container name is the runtime ID everywhere (matching k8s pod
	// names); the docker API accepts names for start/inspect/remove.
	name := "buildkitd-" + spec.Resources.SpecTag() + "-" + randID()[:8]
	if _, err := p.cli.ContainerCreate(ctx, cfg, hc, nil, nil, name); err != nil {
		return "", fmt.Errorf("docker provision: create: %w", err)
	}
	return name, nil
}

// waitReady polls until the buildkit gRPC endpoint actually answers Info.
// The docker host port binds (and accepts, then resets) before buildkitd
// listens, so a port check alone races.
func (p *Provider) waitReady(ctx context.Context, cid string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		insp, err := p.cli.ContainerInspect(ctx, cid)
		if err == nil && insp.State != nil && insp.State.Running {
			binds := insp.NetworkSettings.Ports[buildkitPort]
			if len(binds) > 0 && binds[0].HostPort != "" {
				port := binds[0].HostPort
				if grpcReady(ctx, p.addr(port)) {
					return port, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("docker provision: wait ready: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("docker provision: ready timeout for %s", cid)
}

func grpcReady(ctx context.Context, addr string) bool {
	c, err := bkclient.New(ctx, addr)
	if err != nil {
		return false
	}
	defer c.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err = c.Info(pingCtx)
	return err == nil
}

// Reconcile adopts buildkitd containers left over from a previous daemon
// life back into the pool as warm runtimes. This is safe in our topology:
// the daemon is the sole solve-session owner, so no orphan can have an
// in-flight build — jobs died with the old process. Adopting preserves
// buildkit layer caches across restarts. Stopped or unhealthy containers
// are disposed. Call once at daemon startup, before serving traffic.
func (p *Provider) Reconcile(ctx context.Context) ([]*runtime.Runtime, error) {
	list, err := p.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "buildfleet=runtime")),
	})
	if err != nil {
		return nil, fmt.Errorf("docker reconcile: list: %w", err)
	}
	var adopted []*runtime.Runtime
	for _, c := range list {
		if gen := c.Labels["buildfleet/config-gen"]; gen != p.cfg.ConfigGen {
			// Stale configuration (old image/mode/registry settings):
			// shed it instead of adopting.
			slog.Info("reconcile: shedding stale-config runtime", "runtime", c.ID,
				"configGen", gen, "currentGen", p.cfg.ConfigGen, "names", c.Names)
			if err := p.Dispose(ctx, c.ID); err != nil {
				return adopted, fmt.Errorf("docker reconcile: dispose stale %s: %w", c.ID, err)
			}
			continue
		}
		rt, ok := p.adopt(ctx, c.ID)
		if !ok {
			slog.Info("reconcile: disposing unhealthy orphan", "runtime", c.ID)
			if err := p.Dispose(ctx, c.ID); err != nil {
				return adopted, fmt.Errorf("docker reconcile: dispose %s: %w", c.ID, err)
			}
			continue
		}
		slog.Info("reconcile: adopted runtime", "runtime", c.ID, "addr", rt.Addr)
		adopted = append(adopted, rt)
	}
	return adopted, nil
}

// adopt re-registers one orphaned container when it is running, healthy,
// and has a host port; resources are recovered from the container's own
// limits. Platforms/capabilities stay empty: adopted instances only serve
// requests that do not pin a platform.
func (p *Provider) adopt(ctx context.Context, id string) (*runtime.Runtime, bool) {
	insp, err := p.cli.ContainerInspect(ctx, id)
	if err != nil || insp.State == nil || !insp.State.Running {
		return nil, false
	}
	id = strings.TrimPrefix(insp.Name, "/")
	binds := insp.NetworkSettings.Ports[buildkitPort]
	if len(binds) == 0 || binds[0].HostPort == "" || !grpcReady(ctx, p.addr(binds[0].HostPort)) {
		return nil, false
	}
	spec := runtime.Spec{
		ID:        id,
		Resources: resource.Resources{CPU: float64(insp.HostConfig.NanoCPUs) / 1e9, Memory: resource.Bytes(insp.HostConfig.Memory)},
	}
	p.trackLive(spec.Resources)
	p.mu.Lock()
	p.containers[id] = containerRec{resources: spec.Resources}
	p.mu.Unlock()
	return &runtime.Runtime{
		ID:    id,
		Spec:  spec,
		Addr:  p.addr(binds[0].HostPort),
		State: runtime.Warm,
	}, true
}

// Dispose is idempotent per the Provider contract: removing an already-gone
// container returns nil.
func (p *Provider) Dispose(ctx context.Context, id string) error {
	err := p.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
	p.mu.Lock()
	rec, ok := p.containers[id]
	delete(p.containers, id)
	p.mu.Unlock()
	if ok {
		p.untrackLive(rec.resources)
	}
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

// randID matches the k8s provider naming: lowercase (RFC 1123-friendly).
func randID() string {
	return strings.ToLower(rand.Text())
}

var _ runtime.Provider = (*Provider)(nil)
