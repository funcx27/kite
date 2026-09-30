package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/funcx27/kite/modules/buildfleet/resource"
)

type fakeProvider struct {
	specs []resource.Resources
	got   []resource.Resources
}

func (f *fakeProvider) Provision(ctx context.Context, spec Spec) (*Runtime, error) {
	f.got = append(f.got, spec.Resources)
	return &Runtime{ID: spec.ID, Spec: spec, Addr: "127.0.0.1:1", State: Warm}, nil
}
func (f *fakeProvider) Dispose(ctx context.Context, id string) error { return nil }
func (f *fakeProvider) Capacity() resource.Resources {
	total := resource.Resources{}
	for _, r := range f.specs {
		total = total.Add(r)
	}
	used := resource.Resources{}
	for _, r := range f.got {
		used = used.Add(r)
	}
	return total.Sub(used)
}

var smallSpec = Spec{ID: "r1", Resources: resource.Resources{CPU: 1, Memory: 2 * resource.Giga}, Platforms: []string{"linux/amd64"}, StartupCost: time.Second}

func TestPoolAcquireRelease(t *testing.T) {
	p := NewPool(&fakeProvider{specs: []resource.Resources{{CPU: 4, Memory: 8 * resource.Giga}}})
	p.Register(&Runtime{ID: "warm1", Spec: smallSpec, Addr: "a", State: Warm, lastReleased: time.Now()})

	lease, err := p.Acquire(context.Background(), Spec{Resources: smallSpec.Resources, Platforms: smallSpec.Platforms})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Runtime().ID != "warm1" {
		t.Fatalf("got %s want warm1", lease.Runtime().ID)
	}
	if lease.Runtime().State != Busy {
		t.Fatal("acquired runtime must be busy")
	}

	// warm pool now empty: same-size request must provision
	lease2, err := p.Acquire(context.Background(), Spec{Resources: smallSpec.Resources})
	if err != nil {
		t.Fatal(err)
	}
	lease2.Release()
	lease.Release()

	// after release both are warm and reusable
	lease3, err := p.Acquire(context.Background(), Spec{Resources: smallSpec.Resources})
	if err != nil {
		t.Fatal(err)
	}
	lease3.Release()
}

func TestPoolHardFilter(t *testing.T) {
	p := NewPool(&fakeProvider{})
	p.Register(&Runtime{ID: "drain", Spec: smallSpec, State: Draining, lastReleased: time.Now()})
	p.Register(&Runtime{ID: "smallplat", Spec: smallSpec, State: Warm, lastReleased: time.Now()})

	// draining never matched
	if _, err := p.Acquire(context.Background(), Spec{Resources: smallSpec.Resources, Platforms: []string{"linux/arm64"}}); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("want ErrNoMatch got %v", err)
	}
}

func TestPoolMarkFailed(t *testing.T) {
	p := NewPool(&fakeProvider{specs: []resource.Resources{{CPU: 4, Memory: 8 * resource.Giga}}})
	r := &Runtime{ID: "f1", Spec: smallSpec, State: Warm, lastReleased: time.Now()}
	p.Register(r)
	lease, _ := p.Acquire(context.Background(), Spec{Resources: smallSpec.Resources})
	lease.Fail()
	if r.State != Failed {
		t.Fatalf("want Failed got %s", r.State)
	}
}

func TestPoolReconcile(t *testing.T) {
	p := NewPool(&fakeProvider{specs: []resource.Resources{{CPU: 4, Memory: 8 * resource.Giga}}})
	p.Register(&Runtime{ID: "stale", Spec: smallSpec, State: Busy, lastReleased: time.Now()})
	p.ReconcileDrain() // restart recovery: everything goes draining
	n := 0
	p.forEach(func(r *Runtime) {
		if r.State != Draining {
			t.Fatalf("want Draining got %s", r.State)
		}
		n++
	})
	if n != 1 {
		t.Fatalf("want 1 runtime got %d", n)
	}
}

func TestAcquireAppliesDefaultLimits(t *testing.T) {
	fp := &fakeProvider{specs: []resource.Resources{{CPU: 16, Memory: 32 * resource.Giga}}}
	p := NewPool(fp)

	lease, err := p.Acquire(context.Background(), Spec{}) // zero-value resources
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	got := fp.got[0]
	if got.CPU != 2 || got.Memory != 4*resource.Giga {
		t.Fatalf("zero resources must get default limits: got %+v", got)
	}

	// explicit resources pass through untouched
	lease2, err := p.Acquire(context.Background(), Spec{Resources: resource.Resources{CPU: 4, Memory: 8 * resource.Giga}})
	if err != nil {
		t.Fatal(err)
	}
	defer lease2.Release()
	if fp.got[1].CPU != 4 || fp.got[1].Memory != 8*resource.Giga {
		t.Fatalf("explicit resources must pass through: got %+v", fp.got[1])
	}
}

func TestReapIdleDisposesStaleWarm(t *testing.T) {
	fp := &fakeProvider{specs: []resource.Resources{{CPU: 4, Memory: 8 * resource.Giga}}}
	p := NewPool(fp)

	stale := &Runtime{ID: "stale", Spec: smallSpec, State: Warm, lastReleased: time.Now().Add(-time.Hour)}
	fresh := &Runtime{ID: "fresh", Spec: smallSpec, State: Warm, lastReleased: time.Now()}
	busy := &Runtime{ID: "busy", Spec: smallSpec, State: Busy, lastReleased: time.Now().Add(-time.Hour)}
	p.Register(stale)
	p.Register(fresh)
	p.Register(busy)

	p.reapIdle(context.Background(), 15*time.Minute)

	if _, ok := p.runtimes["stale"]; ok {
		t.Fatal("stale warm runtime must be reaped")
	}
	if _, ok := p.runtimes["fresh"]; !ok {
		t.Fatal("fresh warm runtime must survive")
	}
	if _, ok := p.runtimes["busy"]; !ok {
		t.Fatal("busy runtime must never be reaped")
	}
	if stale.State != Draining {
		t.Fatalf("reaped runtime state: %s", stale.State)
	}
}

// countingProvider hands out unique runtime IDs.
type countingProvider struct {
	name string
	cap  resource.Resources
	n    int
}

func (p *countingProvider) Provision(ctx context.Context, spec Spec) (*Runtime, error) {
	p.n++
	return &Runtime{ID: fmt.Sprintf("%s-%d", p.name, p.n), Spec: spec, Addr: "127.0.0.1:1", State: Warm}, nil
}

func (p *countingProvider) Dispose(ctx context.Context, id string) error { return nil }
func (p *countingProvider) Capacity() resource.Resources                 { return p.cap }

func TestProjectAffinity(t *testing.T) {
	p := NewPool(&countingProvider{name: "docker", cap: resource.Resources{CPU: 8, Memory: 8 * resource.Giga}})
	one := resource.Resources{CPU: 1, Memory: resource.Giga}

	l1, err := p.Acquire(context.Background(), Spec{Resources: one, Affinity: "gitea.local/bfadmin/demo"})
	if err != nil {
		t.Fatal(err)
	}
	first := l1.Runtime().ID
	l1.Release()

	// Same key: sticky to the same runtime (held busy for the fallback
	// test below).
	l2, err := p.Acquire(context.Background(), Spec{Resources: one, Affinity: "gitea.local/bfadmin/demo"})
	if err != nil {
		t.Fatal(err)
	}
	if l2.Runtime().ID != first {
		t.Fatalf("affinity miss: got %s want %s", l2.Runtime().ID, first)
	}

	// A different key provisions a second instance (released: warm).
	l3, err := p.Acquire(context.Background(), Spec{Resources: one, Affinity: "other/project"})
	if err != nil {
		t.Fatal(err)
	}
	second := l3.Runtime().ID
	if second == first {
		t.Fatal("distinct keys should use distinct runtimes")
	}
	l3.Release()

	// Affinity instance Busy (l2 held): falls back to another instance.
	l4, err := p.Acquire(context.Background(), Spec{Resources: one, Affinity: "gitea.local/bfadmin/demo"})
	if err != nil {
		t.Fatal(err)
	}
	if l4.Runtime().ID == first {
		t.Fatal("busy affinity instance should not be handed out")
	}
	if l4.Runtime().ID != second {
		t.Fatalf("fallback should prefer the other warm instance, got %s", l4.Runtime().ID)
	}
	l4.Release()

	l2.Release()

	// Instance removal clears affinity entries.
	p.mu.Lock()
	rt := p.runtimes[first]
	rt.State = Failed
	p.mu.Unlock()
	p.reapIdleFor(p.provider, context.Background(), 0)
	p.mu.Lock()
	_, ok := p.affinity["gitea.local/bfadmin/demo"]
	p.mu.Unlock()
	if ok {
		t.Fatal("affinity entry must die with its runtime")
	}
}

func TestAffinityRespectsProviderPin(t *testing.T) {
	a := &tinyProvider{name: "a", cap: resource.Resources{CPU: 8, Memory: 8 * resource.Giga}}
	b := &tinyProvider{name: "b", cap: resource.Resources{CPU: 8, Memory: 8 * resource.Giga}}
	m := MultiProvider(NamedProvider{Name: "docker-a", Provider: a}, NamedProvider{Name: "k8s-b", Provider: b})
	p := NewPool(m)
	one := resource.Resources{CPU: 1, Memory: resource.Giga}

	l1, err := p.Acquire(context.Background(), Spec{Resources: one, Provider: "docker-a", Affinity: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	first := l1.Runtime().ID
	l1.Release()

	// Pinned elsewhere: the affinity instance (owned by docker-a) must
	// not be picked even though it is warm.
	l2, err := p.Acquire(context.Background(), Spec{Resources: one, Provider: "k8s-b", Affinity: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if l2.Runtime().ID == first {
		t.Fatal("provider pin must take precedence over affinity")
	}
	l2.Release()
}

func TestReaperWorksAcrossMulti(t *testing.T) {
	a := &countingProvider{name: "a", cap: resource.Resources{CPU: 8, Memory: 8 * resource.Giga}}
	p := NewPool(MultiProvider(NamedProvider{Name: "docker-a", Provider: a}))
	l, err := p.Acquire(context.Background(), Spec{Resources: oneCPU})
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	p.StartReaper(context.Background(), 50*time.Millisecond, 100*time.Millisecond)
	time.Sleep(400 * time.Millisecond)
	p.mu.Lock()
	_, ok := p.runtimes[l.Runtime().ID]
	p.mu.Unlock()
	if ok {
		t.Fatal("idle runtime must be reaped when the pool provider is a Multi")
	}
}
