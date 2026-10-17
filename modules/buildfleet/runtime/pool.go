package runtime

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/funcx27/kite/modules/buildfleet/resource"
)

var ErrNoMatch = errors.New("runtime: no matching runtime and no capacity to provision")

// Pool is the in-memory runtime registry. Pure memory state: on restart,
// call ReconcileDrain before serving traffic (DESIGN.md §5.1).
type Pool struct {
	mu       sync.Mutex
	runtimes map[string]*Runtime
	provider Provider
	// defaults is the floor applied to requests: fields the request left
	// at zero get the default, so every provisioned runtime carries real
	// limits (anti-oversubscription, DESIGN.md §8).
	defaults resource.Resources
	// affinity maps project keys to the runtime that last served them;
	// entries die with their runtime (cleaned on removal), so the map
	// is bounded by live instances and needs no TTL.
	affinity map[string]string
}

// NewPool creates a pool with the built-in default limits (2C/4Gi).
func NewPool(p Provider) *Pool {
	return &Pool{
		runtimes: map[string]*Runtime{},
		provider: p,
		defaults: resource.Resources{CPU: 2, Memory: 4 * resource.Giga},
		affinity: map[string]string{},
	}
}

// SetDefaults overrides the default per-instance limits (config runtime).
func (p *Pool) SetDefaults(def resource.Resources) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.defaults = def
}

// Register adds a runtime to the pool (called by providers after readiness).
func (p *Pool) Register(r *Runtime) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.State == "" {
		r.State = Warm
	}
	if r.lastReleased.IsZero() {
		r.lastReleased = time.Now()
	}
	p.runtimes[r.ID] = r
}

// Acquire returns an exclusive lease on a warm runtime matching spec, or
// provisions one via the provider. Zero-value spec fields match anything;
// Resources below the smallest tier round up to it.
func (p *Pool) Acquire(ctx context.Context, spec Spec) (*Lease, error) {
	spec.Resources = p.applyDefaults(spec.Resources)
	p.mu.Lock()
	r := p.pickLocked(spec)
	if r != nil {
		r.State = Busy
		p.rememberLocked(spec.Affinity, r.ID)
		p.mu.Unlock()
		return &Lease{rt: r, mu: &p.mu}, nil
	}
	p.mu.Unlock()

	// No match: provision out of the pool lock (provisioning is slow).
	// Capacity check is advisory; the real adjudication happens at the
	// provider (k8s apiserver quota, docker run failure).
	if !p.provider.Capacity().Fits(spec.Resources) {
		return nil, ErrNoMatch
	}
	rt, err := p.provider.Provision(ctx, spec)
	if err != nil {
		return nil, err
	}
	p.Register(rt)
	p.mu.Lock()
	rt.State = Busy
	p.rememberLocked(spec.Affinity, rt.ID)
	p.mu.Unlock()
	return &Lease{rt: rt, mu: &p.mu}, nil
}

// applyDefaults fills zero-value request fields from the configured
// defaults; explicitly declared fields pass through unchanged.
func (p *Pool) applyDefaults(req resource.Resources) resource.Resources {
	p.mu.Lock()
	def := p.defaults
	p.mu.Unlock()
	if req.CPU == 0 {
		req.CPU = def.CPU
	}
	if req.Memory == 0 {
		req.Memory = def.Memory
	}
	if req.EphemeralStorage == 0 {
		req.EphemeralStorage = def.EphemeralStorage
	}
	return req
}

// pickLocked applies hard filters then orders by startup penalty and
// most-recently-released (LRU keep-alive).
// (DESIGN.md §3: no bin-packing score — warm instances are idle instances.)
func (p *Pool) pickLocked(spec Spec) *Runtime {
	var cands []*Runtime
	for _, r := range p.runtimes {
		if r.State != Warm {
			continue
		}
		if spec.Provider != "" && p.OwnerName(r.ID) != spec.Provider {
			continue
		}
		if !r.Spec.Resources.Fits(spec.Resources) {
			continue
		}
		if !subset(r.Spec.Platforms, spec.Platforms) {
			continue
		}
		if !subset(r.Spec.Capabilities, spec.Capabilities) {
			continue
		}
		cands = append(cands, r)
	}
	if len(cands) == 0 {
		return nil
	}
	sticky := p.affinity[spec.Affinity]
	sort.Slice(cands, func(i, j int) bool {
		// Project affinity first (its local cache mounts hold the
		// project's dependency caches), then startup cost, then LRU.
		ai, aj := cands[i].ID == sticky, cands[j].ID == sticky
		if ai != aj {
			return ai
		}
		if cands[i].Spec.StartupCost != cands[j].Spec.StartupCost {
			return cands[i].Spec.StartupCost < cands[j].Spec.StartupCost
		}
		return cands[i].lastReleased.Before(cands[j].lastReleased)
	})
	return cands[0]
}

// rememberLocked records the project->runtime affinity (empty key skips).
func (p *Pool) rememberLocked(key, id string) {
	if key != "" {
		p.affinity[key] = id
	}
}

// forgetLocked drops affinity entries pointing at a removed runtime.
func (p *Pool) forgetLocked(id string) {
	for k, v := range p.affinity {
		if v == id {
			delete(p.affinity, k)
		}
	}
}

func subset(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	m := map[string]bool{}
	for _, h := range have {
		m[h] = true
	}
	for _, w := range want {
		if !m[w] {
			return false
		}
	}
	return true
}

// Availability reports whether Acquire could succeed right now for spec
// (a matching warm runtime exists, or some provider has spare capacity),
// along with the tier-rounded request and current spare capacity for
// diagnostics. Callers use it to reject work before paying materialization
// costs (e.g. a 16s git clone).
func (p *Pool) Availability(spec Spec) (ok bool, req, avail resource.Resources) {
	spec.Resources = p.applyDefaults(spec.Resources)
	req = spec.Resources
	p.mu.Lock()
	if r := p.pickLocked(spec); r != nil {
		p.mu.Unlock()
		return true, req, p.provider.Capacity()
	}
	p.mu.Unlock()
	avail = p.provider.Capacity()
	return avail.Fits(req), req, avail
}

// StartReaper periodically disposes warm runtimes idle longer than idleTTL,
// returning their capacity to the provider (DESIGN.md §4.3 pool scaling).
// Busy runtimes are never touched.
func (p *Pool) StartReaper(ctx context.Context, interval, idleTTL time.Duration) {
	p.StartReaperFor(p.provider, ctx, interval, idleTTL)
}

// StartReaperFor reaps only the runtimes of one provider, so a mixed pool
// can give docker and k8s different idle TTLs.
func (p *Pool) StartReaperFor(provider Provider, ctx context.Context, interval, idleTTL time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.reapIdleFor(provider, ctx, idleTTL)
			}
		}
	}()
}

func (p *Pool) reapIdle(ctx context.Context, idleTTL time.Duration) {
	p.reapIdleFor(p.provider, ctx, idleTTL)
}

func (p *Pool) reapIdleFor(provider Provider, ctx context.Context, idleTTL time.Duration) {
	p.mu.Lock()
	now := time.Now()
	var victims []*Runtime
	// Expired warm candidates bucketed by owning provider, plus each
	// bucket's total warm count: a pass that would take a provider's last
	// warm instance holds one back (min-1 keep-alive), so the next build
	// reuses it instead of paying a cold provision.
	expired := map[string][]*Runtime{}
	warm := map[string]int{}
	for _, r := range p.runtimes {
		if !p.ownedBy(provider, r) {
			continue
		}
		switch {
		case r.State == Failed:
			// Failed runtimes are never reusable; dispose promptly to
			// return capacity instead of waiting for manual Drain.
			r.State = Draining
			victims = append(victims, r)
		case r.State == Warm:
			key := p.OwnerName(r.ID)
			warm[key]++
			if now.Sub(r.lastReleased) > idleTTL {
				expired[key] = append(expired[key], r)
			}
		}
	}
	for key, cands := range expired {
		if len(cands) == warm[key] {
			// Every warm instance of this provider is past the TTL; keep
			// the most recently released one — it has the longest idle
			// runway before the next pass.
			sort.Slice(cands, func(i, j int) bool {
				return cands[i].lastReleased.Before(cands[j].lastReleased)
			})
			cands = cands[:len(cands)-1]
		}
		for _, r := range cands {
			r.State = Draining // block reacquisition while disposing
			victims = append(victims, r)
		}
	}
	p.mu.Unlock()

	for _, r := range victims {
		if err := provider.Dispose(ctx, r.ID); err != nil {
			// Leave it draining: never reacquired; surface the failure.
			slog.Error("runtime: reap dispose failed", "runtime", r.ID, "err", err)
			continue
		}
		p.mu.Lock()
		delete(p.runtimes, r.ID)
		p.forgetLocked(r.ID)
		p.mu.Unlock()
		slog.Info("runtime: reaped idle runtime", "runtime", r.ID, "idle", now.Sub(r.lastReleased).Round(time.Second))
	}
	if n := len(victims); n > 0 {
		slog.Info("runtime: reap pass done", "disposed", n, "remaining", len(p.runtimes))
	}
}

// ReconcileDrain marks every runtime draining: used after process restart
// when lease ownership is unrecoverable (in-flight builds are not adopted;
// callers recover via the retry chain).
func (p *Pool) ReconcileDrain() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range p.runtimes {
		if r.State == Busy || r.State == Warm {
			r.State = Draining
		}
	}
}

// Drain empties draining/failed runtimes by disposing them via the provider.
func (p *Pool) Drain(ctx context.Context) error {
	p.mu.Lock()
	var ids []string
	for id, r := range p.runtimes {
		if r.State == Draining || r.State == Failed {
			ids = append(ids, id)
			delete(p.runtimes, id)
			p.forgetLocked(id)
		}
	}
	p.mu.Unlock()
	for _, id := range ids {
		if err := p.provider.Dispose(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// Stat is a pool state snapshot entry for observability (DESIGN.md §9).
type Stat struct {
	ID    string
	State State
	Addr  string
	// NodeName is the cluster node the runtime pod runs on (k8s only).
	NodeName string
	// Owner is the config name of the provider that provisioned the
	// runtime (empty for unnamed single-provider pools).
	Owner   string
	CPU     float64
	Memory  resource.Bytes
	IdleFor time.Duration // since last release; 0 while busy
	// LastProject is the project key this runtime last served (empty
	// when the pool's provider does not track affinity).
	LastProject string
}

func (p *Pool) Stats() []Stat {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	out := make([]Stat, 0, len(p.runtimes))
	for _, r := range p.runtimes {
		s := Stat{
			ID:       r.ID,
			State:    r.State,
			Addr:     r.Addr,
			NodeName: r.NodeName,
			CPU:      r.Spec.Resources.CPU,
			Memory:   r.Spec.Resources.Memory,
		}
		s.Owner = p.OwnerName(r.ID)
		if r.State == Warm {
			s.IdleFor = now.Sub(r.lastReleased).Round(time.Second)
		}
		for k, v := range p.affinity {
			if v == r.ID {
				s.LastProject = k
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// OwnerName returns the owning provider's config name for a runtime ID,
// or "" when the pool's provider does not track ownership.
func (p *Pool) OwnerName(id string) string {
	if m, ok := p.provider.(*Multi); ok {
		return m.OwnerName(id)
	}
	return ""
}

// ownedBy reports whether the provider owns this runtime. Single-provider
// pools always true; Multi routes by its recorded ownership.
func (p *Pool) ownedBy(provider Provider, r *Runtime) bool {
	if m, ok := p.provider.(*Multi); ok {
		// The Multi itself (StartReaper's whole-pool sweep) owns
		// everything it delegates; per-provider sweeps compare sub
		// providers via the ownership map.
		if provider == p.provider {
			return true
		}
		return m.Owner(r.ID) == provider
	}
	return true
}

func (p *Pool) forEach(fn func(*Runtime)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range p.runtimes {
		fn(r)
	}
}
