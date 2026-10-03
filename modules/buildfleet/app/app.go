// Package app assembles the buildfleet daemon's runtime chain — providers,
// pool, store, fleet — as a reusable constructor, so the daemon main and an
// embedding host share one ordered setup path.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/funcx27/kite/modules/buildfleet/config"
	"github.com/funcx27/kite/modules/buildfleet/fleet"
	"github.com/funcx27/kite/modules/buildfleet/provider/docker"
	"github.com/funcx27/kite/modules/buildfleet/provider/k8s"
	"github.com/funcx27/kite/modules/buildfleet/runtime"
	"github.com/funcx27/kite/modules/buildfleet/store"
)

// Store blob GC bounds: unreferenced blobs older than gcTTL are reclaimed
// once per gcInterval.
const (
	gcTTL      = 24 * time.Hour
	gcInterval = time.Hour
)

// App carries the assembled daemon pieces a host wires into its own serving
// stack.
type App struct {
	Fleet *fleet.Fleet
	Pool  *runtime.Pool
	Store *store.LocalStore
	// CacheStatFn aggregates per-provider cache registry usage; nil when
	// no provider enables a k8s cache registry.
	CacheStatFn func(ctx context.Context) (map[string]*runtime.CacheStatSnapshot, error)

	// multi is the composed provider Pool provisions through; runtime
	// provider registration (host-managed build providers) mutates it.
	multi *runtime.Multi
	// cfg is retained so runtime registration reuses the loaded runtime
	// profile and registry table.
	cfg *config.Config

	// gcDone closes when the periodic GC goroutine has exited.
	gcDone chan struct{}
}

// Setup assembles providers, pool, store and fleet in daemon start order.
// ctx bounds the reaper, reconcile, cache-registry setup and the periodic
// store GC; the host owns its lifetime.
func Setup(ctx context.Context, cfg *config.Config) (*App, error) {
	providers, err := buildProviders(cfg)
	if err != nil {
		return nil, err
	}
	multi := runtime.MultiProvider(namedProviders(cfg, providers)...)
	pool := runtime.NewPool(multi)
	pool.SetDefaults(cfg.Runtimes["default"].DefaultResources.MustParse())
	if ttl := cfg.Runtimes["default"].IdleTTLParsed(); ttl > 0 {
		pool.StartReaper(ctx, time.Minute, ttl)
		slog.Info("idle reaper enabled", "ttl", ttl)
	}

	for i, p := range providers {
		var adopted []*runtime.Runtime
		var err error
		switch v := p.(type) {
		case *docker.Provider:
			adopted, err = v.Reconcile(ctx)
		case *k8s.Provider:
			adopted, err = v.Reconcile(ctx)
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
		if err := kp.EnsureCacheRegistry(ctx); err != nil {
			return nil, fmt.Errorf("ensure cache registry: provider %s: %w", pc.Name, err)
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

	a := &App{Fleet: f, Pool: pool, Store: st, multi: multi, cfg: cfg}
	k8sCacheProviders := map[string]*k8s.Provider{}
	for i, pc := range cfg.Providers {
		if pc.Type == "k8s" && pc.Cache != nil && pc.Cache.Enabled {
			k8sCacheProviders[pc.Name] = providers[i].(*k8s.Provider)
		}
	}
	// Periodic store GC runs on the setup context: hosts and the daemon
	// pass a long-lived ctx here, so recycling repeats for the process
	// lifetime instead of only during a shutdown drain window.
	a.gcDone = make(chan struct{})
	go func() {
		defer close(a.gcDone)
		a.runGC(ctx)
	}()
	if len(k8sCacheProviders) > 0 {
		a.CacheStatFn = cacheStatFn(k8sCacheProviders)
	}
	return a, nil
}

// RegisterK8sProvider constructs a k8s provider from pc (Kubeconfig must
// point at a readable file, or be empty for in-cluster identity) and attaches
// it to the pool at runtime. Reconcile adopts buildkitd instances a previous
// process left behind, so re-registering a persisted provider after a restart
// preserves layer caches. The provider runs on the default runtime profile.
func (a *App) RegisterK8sProvider(ctx context.Context, pc config.Provider) error {
	regTable, err := effectiveRegTable(a.cfg)
	if err != nil {
		return fmt.Errorf("provider %s: %w", pc.Name, err)
	}
	p, err := buildK8sProvider(a.cfg, pc, regTable, hostAliases(a.cfg.Runtimes["default"].Hosts))
	if err != nil {
		return fmt.Errorf("provider %s: %w", pc.Name, err)
	}
	if err := a.multi.Register(runtime.NamedProvider{Name: pc.Name, Provider: p}); err != nil {
		return fmt.Errorf("provider %s: %w", pc.Name, err)
	}
	adopted, err := p.Reconcile(ctx)
	if err != nil {
		slog.Warn("reconcile leftovers", "provider", pc.Name, "err", err)
	}
	for _, rt := range adopted {
		a.multi.Claim(rt.ID, runtime.NamedProvider{Name: pc.Name, Provider: p})
		a.Pool.Register(rt)
	}
	slog.Info("registered k8s build provider", "provider", pc.Name, "namespace", pc.Namespace)
	return nil
}

// RemoveProvider detaches a runtime-registered provider by name. Live
// runtimes it owns keep draining through the ownership map; unknown names
// return an error.
func (a *App) RemoveProvider(name string) error {
	if err := a.multi.Remove(name); err != nil {
		return fmt.Errorf("remove provider: %w", err)
	}
	slog.Info("removed k8s build provider", "provider", name)
	return nil
}

// Drain disposes draining/failed pool runtimes, waits out an in-flight
// periodic GC pass, then runs one final store GC pass — both waits bounded
// by ctx. The Setup context is expected to be cancelled before Drain, so an
// in-flight pass aborts within one blob and this final one covers blobs that
// became unreferenced at shutdown.
func (a *App) Drain(ctx context.Context) error {
	if err := a.Pool.Drain(ctx); err != nil {
		return fmt.Errorf("drain pool: %w", err)
	}
	// Serialise with the periodic pass: two concurrent walks of the blob
	// dir turn the loser's removal into a spurious ENOENT error here.
	if a.gcDone != nil {
		select {
		case <-a.gcDone:
		case <-ctx.Done():
			return fmt.Errorf("wait store gc: %w", ctx.Err())
		}
	}
	gc := store.NewGC(a.Store, gcTTL)
	if err := gc.Run(ctx); err != nil {
		return fmt.Errorf("store gc: %w", err)
	}
	return nil
}

func (a *App) runGC(ctx context.Context) {
	gc := store.NewGC(a.Store, gcTTL)
	for {
		// A cancelled ctx aborts the pass mid-walk; that is shutdown,
		// not a failure worth logging.
		if err := gc.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("store gc pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(gcInterval):
		}
	}
}

func cacheStatFn(kps map[string]*k8s.Provider) func(ctx context.Context) (map[string]*runtime.CacheStatSnapshot, error) {
	return func(ctx context.Context) (map[string]*runtime.CacheStatSnapshot, error) {
		out := map[string]*runtime.CacheStatSnapshot{}
		for name, kp := range kps {
			snap, err := kp.CacheStat(ctx)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			out[name] = snap
		}
		return out, nil
	}
}
