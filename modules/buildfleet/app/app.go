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
}

// Setup assembles providers, pool, store and fleet in daemon start order.
// ctx bounds the reaper, reconcile, and cache-registry setup; the host owns
// its lifetime.
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

	a := &App{Fleet: f, Pool: pool, Store: st}
	k8sCacheProviders := map[string]*k8s.Provider{}
	for i, pc := range cfg.Providers {
		if pc.Type == "k8s" && pc.Cache != nil && pc.Cache.Enabled {
			k8sCacheProviders[pc.Name] = providers[i].(*k8s.Provider)
		}
	}
	// Periodic store GC runs on the setup context: hosts and the daemon
	// pass a long-lived ctx here, so recycling repeats for the process
	// lifetime instead of only during a shutdown drain window.
	go a.runGC(ctx)
	if len(k8sCacheProviders) > 0 {
		a.CacheStatFn = cacheStatFn(k8sCacheProviders)
	}
	return a, nil
}

// Drain disposes draining/failed pool runtimes, then runs one final store GC
// pass bounded by ctx. Periodic recycling is already running on the Setup
// context; this pass only covers blobs that became unreferenced at shutdown.
func (a *App) Drain(ctx context.Context) error {
	if err := a.Pool.Drain(ctx); err != nil {
		return fmt.Errorf("drain pool: %w", err)
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
		if err := gc.Run(ctx); err != nil {
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
