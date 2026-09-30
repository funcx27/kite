// buildfleetd serves the buildfleet API over Connect (gRPC, gRPC-Web and
// HTTP/JSON on one port) with a mixed docker/k8s runtime pool driven by a
// YAML config file.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"gopkg.in/yaml.v3"

	"github.com/funcx27/kite/modules/buildfleet/api"
	buildfleetv1 "github.com/funcx27/kite/modules/buildfleet/api/buildfleetv1"
	buildfleetv1connect "github.com/funcx27/kite/modules/buildfleet/api/buildfleetv1/buildfleetv1connect"
	"github.com/funcx27/kite/modules/buildfleet/config"
	"github.com/funcx27/kite/modules/buildfleet/fleet"
	"github.com/funcx27/kite/modules/buildfleet/provider/docker"
	"github.com/funcx27/kite/modules/buildfleet/provider/k8s"
	"github.com/funcx27/kite/modules/buildfleet/registry"
	"github.com/funcx27/kite/modules/buildfleet/resource"
	"github.com/funcx27/kite/modules/buildfleet/runtime"
	"github.com/funcx27/kite/modules/buildfleet/store"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "status" {
		os.Exit(runStatus(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "cachestat" {
		os.Exit(runCacheStat(os.Args[2:]))
	}
	cfgPath := flag.String("config", "", "config file path (empty = embedded defaults, overlaid when given)")
	addrOverride := flag.String("addr", "", "override config addr")
	printCfg := flag.Bool("print-config", false, "print the effective config and exit")
	flag.Parse()

	if *printCfg {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			slog.Error("config load failed", "err", err)
			os.Exit(1)
		}
		b, err := yaml.Marshal(cfg)
		if err != nil {
			slog.Error("marshal config failed", "err", err)
			os.Exit(1)
		}
		os.Stdout.Write(b)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}
	if *addrOverride != "" {
		cfg.Addr = *addrOverride
	}

	providers, err := buildProviders(cfg)
	if err != nil {
		slog.Error("build providers failed", "err", err)
		os.Exit(1)
	}
	multi := runtime.MultiProvider(namedProviders(cfg, providers)...)
	pool := runtime.NewPool(multi)
	pool.SetDefaults(cfg.Runtimes["default"].DefaultResources.MustParse())
	if ttl := cfg.Runtimes["default"].IdleTTLParsed(); ttl > 0 {
		pool.StartReaper(context.Background(), time.Minute, ttl)
		slog.Info("idle reaper enabled", "ttl", ttl)
	}

	for i, p := range providers {
		var adopted []*runtime.Runtime
		var err error
		switch v := p.(type) {
		case *docker.Provider:
			adopted, err = v.Reconcile(context.Background())
		case *k8s.Provider:
			adopted, err = v.Reconcile(context.Background())
		default:
			continue
		}
		// Memory-only pool: adopt healthy buildkitd instances from a
		// previous daemon life (preserves layer caches), shed the rest.
		if err != nil {
			slog.Warn("reconcile leftovers", "err", err)
		}
		for _, rt := range adopted {
			// Adopted instances bypass Multi.Provision; record which
			// provider owns them so Status and pinning stay correct.
			multi.Claim(rt.ID, runtime.NamedProvider{Name: cfg.Providers[i].Name, Provider: p})
			pool.Register(rt)
		}
		if n := len(adopted); n > 0 {
			slog.Info("adopted warm runtimes from previous life", "count", n)
		}
	}

	st := store.NewLocalStore(cfg.Store)
	providerCache := map[string]string{}
	for i, pc := range cfg.Providers {
		if pc.Type != "k8s" || pc.Cache == nil || !pc.Cache.Enabled {
			continue
		}
		kp := providers[i].(*k8s.Provider)
		if err := kp.EnsureCacheRegistry(context.Background()); err != nil {
			slog.Error("ensure cache registry failed", "provider", pc.Name, "err", err)
			os.Exit(1)
		}
		providerCache[pc.Name] = kp.CacheBaseRef()
	}
	window, err := time.ParseDuration(cfg.DedupeWindow)
	if err != nil {
		slog.Warn("invalid dedupeWindow, disabling result reuse", "value", cfg.DedupeWindow)
		window = 0
	}
	f := fleet.New(pool, nil, fleet.WithStore(st), fleet.WithProviderCache(providerCache),
		fleet.WithDedupeWindow(window))
	k8sCacheProviders := map[string]*k8s.Provider{}
	for i, pc := range cfg.Providers {
		if pc.Type == "k8s" && pc.Cache != nil && pc.Cache.Enabled {
			k8sCacheProviders[pc.Name] = providers[i].(*k8s.Provider)
		}
	}
	var apiOpts []api.ServerOption

	cacheStat := func(ctx context.Context) (map[string]*runtime.CacheStatSnapshot, error) {
		out := map[string]*runtime.CacheStatSnapshot{}
		for name, kp := range k8sCacheProviders {
			snap, err := kp.CacheStat(ctx)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			out[name] = snap
		}
		return out, nil
	}
	if len(k8sCacheProviders) > 0 {
		apiOpts = append(apiOpts, api.WithCacheStat(cacheStat))
	}
	path, handler := buildfleetv1connect.NewBuildServiceHandler(api.NewServer(f, st, apiOpts...))
	mux := http.NewServeMux()
	mux.Handle(path, handler)

	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true) // native gRPC on cleartext
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		Protocols:         &protos,
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("buildfleetd listening", "addr", cfg.Addr, "protocols", "gRPC+gRPC-Web+HTTP/JSON", "providers", strings.Join(providerTypes(cfg), " -> "))
	slog.Error("server exited", "err", srv.ListenAndServe())
}

// buildProviders instantiates providers in config order (provisioning
// preference).
func buildProviders(cfg *config.Config) ([]runtime.Provider, error) {
	regTable, err := cfg.Runtimes["default"].RegistryTable()
	if err != nil {
		return nil, err
	}
	// In-cluster cache registries must be plain-http reachable from the
	// buildkitd pods; declare them before the toml render below.
	for _, pc := range cfg.Providers {
		if pc.Type != "k8s" || pc.Cache == nil || !pc.Cache.Enabled {
			continue
		}
		regTable = append(regTable, registry.Config{
			Host: k8s.CacheRegistryHost(pc.Namespace), HTTP: true,
		})
	}
	for name, rt := range cfg.Runtimes {
		if rt.Rootless() && !strings.Contains(rt.Image(), "rootless") {
			slog.Warn("runtime image does not look like a rootless image (no \"rootless\" in ref); "+
				"rootful images fail under mode=rootless with mount permission errors",
				"profile", name, "image", rt.Image())
		}
	}
	var out []runtime.Provider
	extraHosts := extraHosts(cfg.Runtimes["default"].Hosts)
	hostAliases := hostAliases(cfg.Runtimes["default"].Hosts)
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
			out = append(out, k8s.New(k8s.Config{
				Namespace:     pc.Namespace,
				CacheRegistry: cacheCfg,
				Clientset:     cs,
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
			}))
		}
	}
	return out, nil
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

func providerTypes(cfg *config.Config) []string {
	types := make([]string, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		types = append(types, p.Name)
	}
	return types
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

// runStatus is a thin client for the daemon's Status RPC:
// buildfleetd status [-addr host:port].
func runStatus(args []string) int {
	client, addr, code := dialDaemon("status", args)
	if client == nil {
		return code
	}
	resp, err := client.Status(context.Background(), connect.NewRequest(&buildfleetv1.StatusRequest{}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: rpc %s: %v\n", addr, err)
		return 1
	}
	out := resp.Msg

	fmt.Printf("%-34s %-14s %-8s %5s %8s %7s  %s\n",
		"RUNTIME", "PROVIDER", "STATE", "CPU", "MEMORY", "IDLE", "ADDR")
	for _, r := range out.Runtimes {
		idle := "-"
		if r.IdleSeconds > 0 {
			idle = fmt.Sprintf("%ds", r.IdleSeconds)
		}
		fmt.Printf("%-34s %-14s %-8s %5.4g %8s %7s  %s\n",
			r.Id, or(r.Provider, "-"), r.State, r.Cpu,
			resource.Bytes(r.MemoryBytes), idle, r.Addr)
	}
	if len(out.Tasks) > 0 {
		fmt.Printf("\nIN-FLIGHT BUILDS\n%-34s %-4s %-12s %s\n", "RUNTIME", "TRY", "OUTPUT", "SOURCE")
		for _, t := range out.Tasks {
			started := time.Unix(t.StartedUnix, 0).Format("15:04:05")
			fmt.Printf("%-34s %-4d %-12s %s (since %s)\n",
				t.RuntimeId, t.Attempt, t.OutputRef, t.Source, started)
		}
	}
	if len(out.Runtimes) == 0 {
		fmt.Println("(no runtimes)")
	}
	return 0
}

// dialDaemon parses the shared -addr flag for query subcommands.
func dialDaemon(name string, args []string) (buildfleetv1connect.BuildServiceClient, string, int) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8484", "daemon address")
	if err := fs.Parse(args); err != nil {
		return nil, "", 2
	}
	return buildfleetv1connect.NewBuildServiceClient(&http.Client{}, "http://"+*addr), *addr, 0
}

// humanBytes renders sizes with one fraction digit (27.0Mi, 1.5Gi).
func humanBytes(n int64) string {
	switch {
	case n >= int64(resource.Giga):
		return fmt.Sprintf("%.1fGi", float64(n)/float64(resource.Giga))
	case n >= int64(resource.Mega):
		return fmt.Sprintf("%.1fMi", float64(n)/float64(resource.Mega))
	case n >= int64(resource.Kilo):
		return fmt.Sprintf("%.1fKi", float64(n)/float64(resource.Kilo))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// runCacheStat prints per-project cache registry usage:
// buildfleetd cachestat [-addr host:port].
func runCacheStat(args []string) int {
	client, addr, code := dialDaemon("cachestat", args)
	if client == nil {
		return code
	}
	resp, err := client.CacheStat(context.Background(), connect.NewRequest(&buildfleetv1.CacheStatRequest{}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "cachestat: rpc %s: %v\n", addr, err)
		return 1
	}
	msg := resp.Msg
	fmt.Printf("%-14s %-28s %5s %7s %10s %9s\n",
		"PROVIDER", "PROJECT", "TAGS", "LAYERS", "EXCLUSIVE", "SHARED")
	for _, p := range msg.Projects {
		fmt.Printf("%-14s %-28s %5d %7d %10s %9s\n",
			p.Provider, p.Project, p.Tags, p.Layers,
			humanBytes(p.ExclusiveBytes), humanBytes(p.SharedBytes))
	}
	fmt.Printf("TOTAL: %s\n", humanBytes(msg.TotalBytes))
	if len(msg.Projects) == 0 {
		fmt.Println("(no cache projects)")
	}
	return 0
}
