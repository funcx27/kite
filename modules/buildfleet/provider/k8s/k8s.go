package k8s

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"k8s.io/utils/ptr"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	k8sresource "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	bkclient "github.com/moby/buildkit/client"

	"github.com/funcx27/kite/modules/buildfleet/registry"
	bfresource "github.com/funcx27/kite/modules/buildfleet/resource"
	"github.com/funcx27/kite/modules/buildfleet/runtime"
)

const (
	grpcPort       = 1234
	configMountDir = "/etc/buildkit"
	configFile     = "buildkitd.toml"
)

type Config struct {
	Namespace string
	// Clientset is injectable; nil makes Capacity fall back to Total.
	Clientset k8sresource.Interface
	// Kubeconfig is the file Clientset/RestConfig were built from; when set,
	// the provider rebuilds them if the file's content changes.
	Kubeconfig string
	Total      bfresource.Resources // fallback when quota unreadable
	Image      string               // global BuildkitImage, never from templates
	Registries registry.Table
	// HostAliases adds static /etc/hosts entries to every pod (intranet
	// git servers and similar).
	HostAliases []corev1.HostAlias
	// BasePod is the declarative pod shape. TODO(v2): per-profile overrides
	// selected via resource.Profile Fit; v1 ships a single base template.
	BasePod *PodTemplate
	// ConfigMapName is the shared buildkitd.toml ConfigMap all pods mount
	// (create-or-update at daemon startup). Default "buildkitd-config".
	ConfigMapName string
	// ReadyTimeout bounds the pod-Ready poll; default 60s.
	ReadyTimeout time.Duration
	// Privileged runs the buildkitd container privileged: rootful images
	// need CAP_SYS_SYSADMIN for snapshotter mounts (same reason the docker
	// provider has a privileged switch).
	Privileged bool

	// Rootless adapts the pod for rootless buildkitd images: adds
	// --oci-worker-no-process-sandbox (runc cannot nest a pid sandbox in a
	// pod) and relaxes seccomp. buildkitd auto-picks the snapshotter:
	// fuse-overlayfs when /dev/fuse is available, else native (no mounts,
	// slower full-copy layers).
	Rootless bool

	// ConfigGen tags provisioned pods with the effective runtime config;
	// future reconcile paths use it to shed stale instances.
	ConfigGen string

	// PortForward addresses runtimes via the apiserver SPDY tunnel
	// (kubectl port-forward mechanics) instead of the Service DNS name.
	// For daemons that can reach the apiserver but not pod networking.
	// Requires RestConfig.
	PortForward bool
	RestConfig  *rest.Config

	// StartupCost is the provider's cold-start penalty (e.g. ECI 10s+),
	// raised onto provisioned/adopted runtime specs so the pool's
	// warm-candidate ordering prefers cheap-start providers (DESIGN.md
	// §5.2.3). Never lowers a caller-declared cost.
	StartupCost time.Duration

	// CacheRegistry deploys the in-cluster layer-cache registry at
	// startup when non-nil; CacheRef exposes its buildkit cache ref.
	CacheRegistry *CacheRegistryConfig
}

// CacheRegistryConfig shapes the auto-deployed cache registry.
type CacheRegistryConfig struct {
	Image        string
	StorageClass string
	Size         string // PVC size quantity, default 20Gi
	// GCSchedule is a cron for the garbage-collect job (retention: the
	// latest cache chain per project). Default "0 3 * * *"; "off"
	// disables the GC job.
	GCSchedule string
}

// Provider supplies buildkitd runtimes as pods plus a ClusterIP Service per
// runtime (DESIGN.md §4.3).
type Provider struct {
	cfg  Config
	mu   sync.Mutex
	live bfresource.Resources
	// tracked maps runtime ID -> resources reserved for that runtime.
	tracked map[string]bfresource.Resources
	// fwds maps runtime ID -> its port-forward stop channel.
	fwds map[string]chan struct{}
	// clientMu guards the apiserver clients and their kubeconfig fingerprint
	// (rebuilt by currentClients when the kubeconfig file changes).
	clientMu         sync.Mutex
	curClient        k8sresource.Interface
	curRest          *rest.Config
	kubeFingerprint  string
}

func New(cfg Config) *Provider {
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = 60 * time.Second
	}
	if cfg.BasePod == nil {
		cfg.BasePod = &PodTemplate{}
	}
	if cfg.ConfigMapName == "" {
		cfg.ConfigMapName = "buildkitd-config"
	}
	return &Provider{cfg: cfg, tracked: map[string]bfresource.Resources{}, fwds: map[string]chan struct{}{},
		curClient: cfg.Clientset, curRest: cfg.RestConfig}
}

// Capacity reads the namespace ResourceQuota; on any error or nil clientset
// it falls back to the configured total minus live-tracked runtimes.
func (p *Provider) Capacity() bfresource.Resources {
	if r, ok := p.quotaCapacity(); ok {
		return r
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.Total.Sub(p.live)
}

func (p *Provider) quotaCapacity() (bfresource.Resources, bool) {
	cs := p.clientset()
	if cs == nil {
		return bfresource.Resources{}, false
	}
	ql, err := cs.CoreV1().ResourceQuotas(p.cfg.Namespace).
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return bfresource.Resources{}, false
	}
	// First quota covering both cpu and memory wins; full multi-quota
	// arbitration is out of MVP scope.
	for i := range ql.Items {
		q := &ql.Items[i]
		hardCPU, okC := q.Status.Hard[corev1.ResourceCPU]
		hardMem, okM := q.Status.Hard[corev1.ResourceMemory]
		if !okC || !okM {
			continue
		}
		usedCPU := q.Status.Used[corev1.ResourceCPU]
		usedMem := q.Status.Used[corev1.ResourceMemory]
		return bfresource.Resources{
			CPU:    float64(hardCPU.MilliValue()-usedCPU.MilliValue()) / 1000,
			Memory: bfresource.Bytes(hardMem.Value() - usedMem.Value()),
		}, true
	}
	return bfresource.Resources{}, false
}

func (p *Provider) trackLive(id string, r bfresource.Resources) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.live = p.live.Add(r)
	p.tracked[id] = r
}

func (p *Provider) untrackLive(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.tracked[id]
	if !ok {
		return
	}
	delete(p.tracked, id)
	p.live = p.live.Sub(r)
}

// Provision creates ConfigMap + Pod + Service, waits for pod Ready, and
// returns a Warm runtime addressed via the Service DNS name.
func (p *Provider) Provision(ctx context.Context, spec runtime.Spec) (*runtime.Runtime, error) {
	spec = p.withStartupCost(spec)
	id := "buildkitd-" + spec.Resources.SpecTag() + "-" + randID()[:8]
	tmpl := Merge(p.cfg.BasePod, nil)

	if err := p.ensureConfigMap(ctx); err != nil {
		return nil, err
	}

	pod := p.buildPod(id, spec, tmpl)
	cs := p.clientset()
	if _, err := cs.CoreV1().Pods(p.cfg.Namespace).
		Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		_ = p.Dispose(context.WithoutCancel(ctx), id)
		return nil, fmt.Errorf("k8s provision: create pod: %w", err)
	}

	svc := p.buildService(id, tmpl)
	if _, err := cs.CoreV1().Services(p.cfg.Namespace).
		Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		_ = p.Dispose(context.WithoutCancel(ctx), id)
		return nil, fmt.Errorf("k8s provision: create service: %w", err)
	}

	p.trackLive(id, spec.Resources)
	nodeName, err := p.waitReady(ctx, id)
	if err != nil {
		_ = p.Dispose(context.WithoutCancel(ctx), id)
		return nil, err
	}
	addr := fmt.Sprintf("%s.%s:%d", id, p.cfg.Namespace, grpcPort)
	if p.cfg.PortForward {
		local, err := p.startForward(id)
		if err != nil {
			_ = p.Dispose(context.WithoutCancel(ctx), id)
			return nil, err
		}
		addr = "127.0.0.1:" + strconv.Itoa(local)
	}
	return &runtime.Runtime{
		ID:       id,
		Spec:     spec,
		Addr:     addr,
		NodeName: nodeName,
		State:    runtime.Warm,
	}, nil
}

// startForward tunnels a local ephemeral port to the runtime pod's gRPC
// port through the apiserver (SPDY upgrade), so the daemon needs no pod
// network access. The tunnel closes in Dispose.
func (p *Provider) startForward(id string) (int, error) {
	port, stop, err := p.forwardTo(id, grpcPort)
	if err != nil {
		return 0, err
	}
	p.mu.Lock()
	p.fwds[id] = stop
	p.mu.Unlock()
	return port, nil
}

// forwardTo opens a temporary SPDY tunnel to one pod port without
// registering it in fwds; the caller owns the returned stop channel.
func (p *Provider) forwardTo(pod string, remotePort int) (int, chan struct{}, error) {
	rc := p.restConfig()
	if rc == nil {
		return 0, nil, fmt.Errorf("k8s port-forward requires RestConfig")
	}
	roundTripper, upgrader, err := spdy.RoundTripperFor(rc)
	if err != nil {
		return 0, nil, fmt.Errorf("k8s port-forward: roundtripper: %w", err)
	}
	host, err := url.Parse(rc.Host)
	if err != nil {
		return 0, nil, fmt.Errorf("k8s port-forward: apiserver url: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost,
		host.JoinPath("api", "v1", "namespaces", p.cfg.Namespace, "pods", pod, "portforward"))

	stop := make(chan struct{})
	pf, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"},
		[]string{fmt.Sprintf("0:%d", remotePort)}, stop, make(chan struct{}), io.Discard, io.Discard)
	if err != nil {
		close(stop)
		return 0, nil, fmt.Errorf("k8s port-forward: new: %w", err)
	}
	go func() {
		// Forward until stopped; errors were logged by earlier callers,
		// discard here to keep stat queries quiet.
		_ = pf.ForwardPorts()
	}()
	select {
	case <-pf.Ready:
		ports, err := pf.GetPorts()
		if err != nil {
			close(stop)
			return 0, nil, fmt.Errorf("k8s port-forward: ports: %w", err)
		}
		return int(ports[0].Local), stop, nil
	case <-time.After(15 * time.Second):
		close(stop)
		return 0, nil, fmt.Errorf("k8s port-forward: ready timeout for %s", pod)
	}
}

// Dispose deletes Pod+Service (the shared ConfigMap stays); idempotent per
// the Provider contract.
func (p *Provider) Dispose(ctx context.Context, id string) error {
	p.untrackLive(id)
	p.mu.Lock()
	if stop, ok := p.fwds[id]; ok {
		close(stop)
		delete(p.fwds, id)
	}
	p.mu.Unlock()
	cs := p.clientset().CoreV1()
	del := func(err error, kind string) error {
		if err == nil || apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("k8s dispose %s %s: %w", kind, id, err)
	}
	if err := del(cs.Pods(p.cfg.Namespace).Delete(ctx, id, metav1.DeleteOptions{}), "pod"); err != nil {
		return err
	}
	return del(cs.Services(p.cfg.Namespace).Delete(ctx, id, metav1.DeleteOptions{}), "service")
}

// waitReady polls the pod Ready condition every 500ms until ReadyTimeout,
// returning the node the ready pod was scheduled onto.
func (p *Provider) waitReady(ctx context.Context, id string) (string, error) {
	deadline := time.Now().Add(p.cfg.ReadyTimeout)
	for time.Now().Before(deadline) {
		pod, err := p.clientset().CoreV1().Pods(p.cfg.Namespace).
			Get(ctx, id, metav1.GetOptions{})
		if err == nil {
			for _, c := range pod.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
					return pod.Spec.NodeName, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("k8s provision: wait ready %s: %w", id, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("k8s provision: ready timeout for %s", id)
}

func (p *Provider) buildPod(id string, spec runtime.Spec, tmpl *PodTemplate) *corev1.Pod {
	rl := corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewMilliQuantity(int64(spec.Resources.CPU*1000), resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(int64(spec.Resources.Memory), resource.BinarySI),
	}
	if spec.Resources.EphemeralStorage > 0 {
		rl[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(int64(spec.Resources.EphemeralStorage), resource.BinarySI)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        id,
			Labels:      mergeMap(tmpl.Labels, map[string]string{"buildfleet": "runtime", "buildfleet-id": id}),
			Annotations: mergeMap(tmpl.Annotations, map[string]string{"buildfleet/config-gen": p.cfg.ConfigGen}),
		},
		Spec: corev1.PodSpec{
			NodeSelector:       tmpl.NodeSelector,
			Tolerations:        tmpl.Tolerations,
			Affinity:           tmpl.Affinity,
			ServiceAccountName: tmpl.ServiceAccount,
			PriorityClassName:  tmpl.PriorityClassName,
			SecurityContext:    tmpl.SecurityContext,
			HostAliases:        p.cfg.HostAliases,
			Volumes: append(append([]corev1.Volume{}, tmpl.Volumes...), corev1.Volume{
				Name: "buildkitd-config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: p.cfg.ConfigMapName},
					},
				},
			}),
			Containers: []corev1.Container{{
				Name:            "buildkitd",
				Image:           p.cfg.Image, // Guaranteed QoS: requests == limits
				Command:         p.buildkitdCommand(),
				SecurityContext: p.containerSecurityContext(),
				Resources: corev1.ResourceRequirements{
					Requests: rl.DeepCopy(),
					Limits:   rl,
				},
				Env: append([]corev1.EnvVar{}, tmpl.Env...),
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "buildkitd-config",
					MountPath: configMountDir,
					ReadOnly:  true,
				}},
			}},
		},
	}
	return pod
}

func (p *Provider) buildkitdCommand() []string {
	var cmd []string
	if p.cfg.Rootless {
		// Rootless images' entrypoint wraps buildkitd in rootlesskit,
		// which sets up the user namespace; invoking buildkitd directly
		// fails with "requires to be executed as the mapped root".
		// --config explicit: do not rely on the default path inside the
		// rootlesskit mount namespace.
		cmd = []string{"rootlesskit", "buildkitd",
			"--config", "/etc/buildkit/buildkitd.toml",
			"--addr", fmt.Sprintf("tcp://0.0.0.0:%d", grpcPort),
			"--oci-worker-no-process-sandbox"}
	} else {
		cmd = []string{"buildkitd", "--addr", fmt.Sprintf("tcp://0.0.0.0:%d", grpcPort)}
	}
	return cmd
}

func (p *Provider) containerSecurityContext() *corev1.SecurityContext {
	switch {
	case p.cfg.Privileged:
		return &corev1.SecurityContext{Privileged: ptr.To(true)}
	case p.cfg.Rootless:
		// FUSE and user-namespace syscalls are blocked by the default
		// profile (same reason docker rootless uses seccomp=unconfined).
		return &corev1.SecurityContext{
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
		}
	default:
		return nil
	}
}

func (p *Provider) buildService(id string, tmpl *PodTemplate) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:   id,
			Labels: mergeMap(tmpl.Labels, map[string]string{"buildfleet": "runtime", "buildfleet-id": id}),
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"buildfleet-id": id},
			Ports: []corev1.ServicePort{{
				Port: grpcPort, TargetPort: intstr.FromInt32(grpcPort),
			}},
		},
	}
}

// ensureConfigMap create-or-updates the shared buildkitd.toml ConfigMap.
// One per namespace, shared by every runtime pod: config content is
// provider-global, and pod restarts pick up current content on recreation.
func (p *Provider) ensureConfigMap(ctx context.Context) error {
	toml, err := registry.RenderToml(p.cfg.Registries)
	if err != nil {
		return fmt.Errorf("k8s provision: render toml: %w", err)
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.cfg.ConfigMapName,
			Namespace: p.cfg.Namespace,
			Labels:    map[string]string{"buildfleet": "runtime"},
		},
		Data: map[string]string{configFile: toml},
	}
	cs := p.clientset().CoreV1()
	_, err = cs.ConfigMaps(p.cfg.Namespace).Create(ctx, cm, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = cs.ConfigMaps(p.cfg.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("k8s provision: ensure configmap: %w", err)
	}
	return nil
}

// randID is lowercase: k8s object names must be RFC 1123 subdomains.
func randID() string {
	return strings.ToLower(rand.Text())
}

var _ runtime.Provider = (*Provider)(nil)

// grpcReady probes the buildkit gRPC endpoint with an Info call; a bare
// port check would race the listener. The generous timeout covers the
// first request over a freshly-established port-forward tunnel.
func grpcReady(ctx context.Context, addr string) bool {
	if !strings.Contains(addr, "://") {
		addr = "tcp://" + addr
	}
	c, err := bkclient.New(ctx, addr)
	if err != nil {
		slog.Debug("k8s grpcReady dial failed", "addr", addr, "err", err)
		return false
	}
	defer c.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = c.Info(pingCtx)
	if err != nil {
		slog.Debug("k8s grpcReady probe failed", "addr", addr, "err", err)
	}
	return err == nil
}

// Reconcile adopts buildkitd pods left over from a previous daemon life
// back into the pool as warm runtimes (preserving layer caches), shedding
// gen-mismatched (stale config) or unhealthy ones. Safe like the docker
// path: the daemon is the sole solve-session owner, so no orphan can have
// an in-flight build. Call once at startup, before serving traffic.
func (p *Provider) Reconcile(ctx context.Context) ([]*runtime.Runtime, error) {
	pods, err := p.clientset().CoreV1().Pods(p.cfg.Namespace).
		List(ctx, metav1.ListOptions{LabelSelector: "buildfleet=runtime"})
	if err != nil {
		return nil, fmt.Errorf("k8s reconcile: list pods: %w", err)
	}
	var adopted []*runtime.Runtime
	for _, pod := range pods.Items {
		if gen := pod.Annotations["buildfleet/config-gen"]; gen != p.cfg.ConfigGen {
			slog.Info("reconcile: shedding stale-config runtime", "runtime", pod.Name,
				"configGen", gen, "currentGen", p.cfg.ConfigGen)
			if err := p.Dispose(ctx, pod.Name); err != nil {
				return adopted, fmt.Errorf("k8s reconcile: dispose stale %s: %w", pod.Name, err)
			}
			continue
		}
		rt, ok := p.adopt(ctx, &pod)
		if !ok {
			slog.Info("reconcile: disposing unhealthy orphan", "runtime", pod.Name)
			if err := p.Dispose(ctx, pod.Name); err != nil {
				return adopted, fmt.Errorf("k8s reconcile: dispose %s: %w", pod.Name, err)
			}
			continue
		}
		slog.Info("reconcile: adopted runtime", "runtime", rt.ID, "addr", rt.Addr)
		adopted = append(adopted, rt)
	}
	return adopted, nil
}

// adopt re-registers one orphaned pod when it is Running and answers
// gRPC; resources are recovered from the pod's own limits.
func (p *Provider) adopt(ctx context.Context, pod *corev1.Pod) (*runtime.Runtime, bool) {
	if pod.Status.Phase != corev1.PodRunning {
		return nil, false
	}
	addr := fmt.Sprintf("%s.%s:%d", pod.Name, p.cfg.Namespace, grpcPort)
	if p.cfg.PortForward {
		local, err := p.startForward(pod.Name)
		if err != nil {
			return nil, false
		}
		addr = "127.0.0.1:" + strconv.Itoa(local)
	}
	if !grpcReady(ctx, addr) {
		if p.cfg.PortForward {
			_ = p.Dispose(context.WithoutCancel(ctx), pod.Name)
		}
		return nil, false
	}
	limits := pod.Spec.Containers[0].Resources.Limits
	spec := runtime.Spec{ID: pod.Name, Resources: bfresource.Resources{
		CPU:    float64(limits.Cpu().MilliValue()) / 1000,
		Memory: bfresource.Bytes(limits.Memory().Value()),
	}}
	spec = p.withStartupCost(spec)
	p.trackLive(pod.Name, spec.Resources)
	return &runtime.Runtime{
		ID:       pod.Name,
		Spec:     spec,
		Addr:     addr,
		NodeName: pod.Spec.NodeName,
		State:    runtime.Warm,
	}, true
}

func (p *Provider) withStartupCost(spec runtime.Spec) runtime.Spec {
	if p.cfg.StartupCost > spec.StartupCost {
		spec.StartupCost = p.cfg.StartupCost
	}
	return spec
}
