package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/funcx27/kite/modules/buildfleet/config"
	"github.com/funcx27/kite/modules/buildfleet/provider/docker"
	"github.com/funcx27/kite/modules/buildfleet/provider/k8s"
	"github.com/funcx27/kite/modules/buildfleet/registry"
	"github.com/funcx27/kite/modules/buildfleet/runtime"
)

// buildProviders instantiates providers in config order (provisioning
// preference).
func buildProviders(cfg *config.Config) ([]runtime.Provider, error) {
	regTable, err := effectiveRegTable(cfg)
	if err != nil {
		return nil, err
	}
	for name, rt := range cfg.RuntimeProfiles() {
		if rt.Rootless() && !strings.Contains(rt.Image(), "rootless") {
			slog.Warn("runtime image does not look like a rootless image (no \"rootless\" in ref); "+
				"rootful images fail under mode=rootless with mount permission errors",
				"profile", name, "image", rt.Image())
		}
	}
	var out []runtime.Provider
	def := cfg.DefaultRuntime()
	extraHosts := extraHosts(def.Hosts)
	hostAliases := hostAliases(def.Hosts)
	// Shared buildkitd.toml for docker runtimes: one file, written once
	// at startup; all containers bind-mount it read-only.
	tomlPath := filepath.Join(cfg.Store, "buildkitd.toml")
	if err := os.MkdirAll(filepath.Dir(tomlPath), 0o755); err != nil {
		return nil, err
	}
	toml, err := registry.RenderToml(regTable)
	if err != nil {
		return nil, err
	}
	// 0644: rootless buildkitd (uid 1000) reads it via bind mount.
	if err := os.WriteFile(tomlPath, []byte(toml), 0o644); err != nil {
		return nil, err
	}

	for _, pc := range cfg.Providers {
		profile := cfg.Profile(pc)
		gen := profile.Gen(regTable)
		total, err := pc.Total.Parse()
		if err != nil {
			return nil, err
		}
		switch pc.Type {
		case "docker":
			dcfg := docker.Config{
				Total:      total,
				Image:      profile.Image(),
				Registries: regTable,
				TomlPath:   tomlPath,
				Address:    pc.Address,
				Privileged: profile.Privileged(),
				ConfigGen:  gen,
				ExtraHosts: extraHosts,
			}
			if pc.TLS != nil {
				dcfg.TLS = &docker.TLSConfig{
					CA:                 pc.TLS.CA,
					Cert:               pc.TLS.Cert,
					Key:                pc.TLS.Key,
					InsecureSkipVerify: pc.TLS.InsecureSkipVerify,
				}
			}
			if profile.Rootless() {
				dcfg.SecurityOpt = append([]string{"seccomp=unconfined"}, pc.SecurityOpt...)
			}
			dp, err := docker.New(dcfg)
			if err != nil {
				return nil, err
			}
			out = append(out, dp)
		case "k8s":
			p, err := buildK8sProvider(cfg, pc, regTable, hostAliases)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// effectiveRegTable renders the shared registry table: insecure entries and
// mirrors from the default runtime, plus every config k8s provider's
// in-cluster cache registry (plain-http reachable from buildkitd pods).
func effectiveRegTable(cfg *config.Config) (registry.Table, error) {
	regTable, err := cfg.DefaultRuntime().RegistryTable()
	if err != nil {
		return nil, err
	}
	for _, pc := range cfg.Providers {
		if pc.Type != "k8s" || pc.Cache == nil || !pc.Cache.Enabled {
			continue
		}
		regTable = append(regTable, registry.Config{
			Host: k8s.CacheRegistryHost(pc.Namespace), HTTP: true,
		})
	}
	return regTable, nil
}

// RegisterDockerProvider constructs a docker provider from pc and attaches it
// to the pool at runtime, mirroring the startup docker branch: the shared
// buildkitd.toml bind-mount means remote tcp daemons are rejected by
// docker.New the same way. The toml itself was written once at startup.
func (a *App) RegisterDockerProvider(_ context.Context, pc config.Provider) error {
	regTable, err := effectiveRegTable(a.cfg)
	if err != nil {
		return fmt.Errorf("provider %s: %w", pc.Name, err)
	}
	total, err := pc.Total.Parse()
	if err != nil {
		return fmt.Errorf("provider %s: %w", pc.Name, err)
	}
	profile := a.cfg.Profile(pc)
	dcfg := docker.Config{
		Total:      total,
		Image:      profile.Image(),
		Registries: regTable,
		TomlPath:   filepath.Join(a.cfg.Store, "buildkitd.toml"),
		Address:    pc.Address,
		Privileged: profile.Privileged(),
		ConfigGen:  profile.Gen(regTable),
		ExtraHosts: extraHosts(a.cfg.DefaultRuntime().Hosts),
	}
	if pc.TLS != nil {
		dcfg.TLS = &docker.TLSConfig{
			CA:                 pc.TLS.CA,
			Cert:               pc.TLS.Cert,
			Key:                pc.TLS.Key,
			InsecureSkipVerify: pc.TLS.InsecureSkipVerify,
		}
	}
	if profile.Rootless() {
		dcfg.SecurityOpt = append([]string{"seccomp=unconfined"}, pc.SecurityOpt...)
	}
	dp, err := docker.New(dcfg)
	if err != nil {
		return fmt.Errorf("provider %s: %w", pc.Name, err)
	}
	if err := a.multi.Register(runtime.NamedProvider{Name: pc.Name, Provider: dp}); err != nil {
		return fmt.Errorf("provider %s: %w", pc.Name, err)
	}
	slog.Info("registered docker build provider", "provider", pc.Name, "address", pc.Address)
	return nil
}

// buildK8sProvider constructs one k8s provider from its config entry; shared
// by startup assembly and runtime registration (host-managed providers).
func buildK8sProvider(cfg *config.Config, pc config.Provider, regTable registry.Table, hostAliases []corev1.HostAlias) (*k8s.Provider, error) {
	profile := cfg.Profile(pc)
	gen := profile.Gen(regTable)
	total, err := pc.Total.Parse()
	if err != nil {
		return nil, err
	}
	kcfg, err := k8s.NewRestConfig(pc.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("k8s provider %s: %w", pc.Name, err)
	}
	cs, err := kubernetes.NewForConfig(kcfg)
	if err != nil {
		return nil, err
	}
	readyTimeout, err := pc.ReadyTimeoutParsed()
	if err != nil {
		return nil, err
	}
	var cacheCfg *k8s.CacheRegistryConfig
	if pc.Cache != nil && pc.Cache.Enabled {
		cacheCfg = &k8s.CacheRegistryConfig{
			Image:        pc.Cache.Image,
			StorageClass: pc.Cache.StorageClass,
			Size:         pc.Cache.Size,
			GCSchedule:   pc.Cache.GCSchedule,
		}
	}
	startupCost, err := pc.StartupCostParsed()
	if err != nil {
		return nil, err
	}
	// Effective pod shape = the declarative podTemplate under the
	// cloud block's translation (e.g. ECI annotations) on top.
	base := k8sPodTemplate(pc.PodTemplate)
	if pc.ECI != nil {
		base = k8s.Merge(base, k8sPodTemplate(pc.ECI.Template()))
	}
	if base != nil {
		gen, err = extendGen(gen, base)
		if err != nil {
			return nil, err
		}
	}
	return k8s.New(k8s.Config{
		Namespace:     pc.Namespace,
		CacheRegistry: cacheCfg,
		Clientset:     cs,
		Kubeconfig:    pc.Kubeconfig,
		RestConfig:    kcfg,
		PortForward:   pc.PortForward,
		BasePod:       base,
		StartupCost:   startupCost,
		Privileged:    profile.Privileged(),
		Rootless:      profile.Rootless(),
		ConfigGen:     gen,
		Total:         total, // Capacity() prefers the namespace ResourceQuota
		Image:         profile.Image(),
		Registries:    regTable,
		HostAliases:   hostAliases,
		ReadyTimeout:  readyTimeout,
	}), nil
}

// k8sPodTemplate converts the config-layer template into the provider's
// field-identical representation.
func k8sPodTemplate(t *config.PodTemplate) *k8s.PodTemplate {
	if t == nil {
		return nil
	}
	return &k8s.PodTemplate{
		Annotations:       t.Annotations,
		Labels:            t.Labels,
		NodeSelector:      t.NodeSelector,
		Tolerations:       t.Tolerations,
		Affinity:          t.Affinity,
		Env:               t.Env,
		Volumes:           t.Volumes,
		SecurityContext:   t.SecurityContext,
		ServiceAccount:    t.ServiceAccount,
		PriorityClassName: t.PriorityClassName,
	}
}

// extendGen fingerprints the effective pod template into the runtime
// config generation, so template or cloud-block changes shed stale pods
// on reconcile, like runtime-profile changes do.
func extendGen(gen string, t *k8s.PodTemplate) (string, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("pod template gen: %w", err)
	}
	sum := sha256.Sum256(b)
	return gen + "+" + hex.EncodeToString(sum[:])[:12], nil
}

func namedProviders(cfg *config.Config, ps []runtime.Provider) []runtime.NamedProvider {
	out := make([]runtime.NamedProvider, 0, len(ps))
	for i, p := range ps {
		out = append(out, runtime.NamedProvider{Name: cfg.Providers[i].Name, Provider: p})
	}
	return out
}

// extraHosts renders hosts entries as docker's "name:ip" form.
func extraHosts(entries []config.HostEntry) []string {
	var out []string
	for _, h := range entries {
		for _, name := range h.Names {
			out = append(out, name+":"+h.IP)
		}
	}
	return out
}

// hostAliases renders the same entries for k8s pods.
func hostAliases(entries []config.HostEntry) []corev1.HostAlias {
	out := make([]corev1.HostAlias, 0, len(entries))
	for _, h := range entries {
		out = append(out, corev1.HostAlias{IP: h.IP, Hostnames: h.Names})
	}
	return out
}
