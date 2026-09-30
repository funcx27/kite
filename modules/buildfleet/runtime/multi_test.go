package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/funcx27/kite/modules/buildfleet/resource"
)

type tinyProvider struct {
	name    string
	cap     resource.Resources
	live    resource.Resources
	dispose []string
}

func (p *tinyProvider) Provision(ctx context.Context, spec Spec) (*Runtime, error) {
	p.live = p.live.Add(spec.Resources)
	return &Runtime{ID: p.name + "-1", Spec: spec, Addr: "127.0.0.1:1", State: Warm}, nil
}

func (p *tinyProvider) Dispose(ctx context.Context, id string) error {
	p.dispose = append(p.dispose, id)
	p.live = resource.Resources{}
	return nil
}

func (p *tinyProvider) Capacity() resource.Resources { return p.cap.Sub(p.live) }

var oneCPU = resource.Resources{CPU: 1, Memory: resource.Giga}

func TestMultiProvisionPreferenceOrder(t *testing.T) {
	preferred := &tinyProvider{name: "k8s", cap: resource.Resources{CPU: 1, Memory: resource.Giga}}
	fallback := &tinyProvider{name: "docker", cap: resource.Resources{CPU: 8, Memory: 8 * resource.Giga}}
	m := MultiProvider(NamedProvider{Name: "k8s", Provider: preferred}, NamedProvider{Name: "docker", Provider: fallback})

	rt, err := m.Provision(context.Background(), Spec{Resources: oneCPU, StartupCost: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if rt.ID != "k8s-1" {
		t.Fatalf("want preferred provider, got %s", rt.ID)
	}

	// preferred is now full: next provision falls through to fallback
	rt2, err := m.Provision(context.Background(), Spec{Resources: oneCPU})
	if err != nil {
		t.Fatal(err)
	}
	if rt2.ID != "docker-1" {
		t.Fatalf("want fallback provider, got %s", rt2.ID)
	}
}

func TestMultiCapacitySumsAndFragments(t *testing.T) {
	a := &tinyProvider{name: "a", cap: resource.Resources{CPU: 1, Memory: resource.Giga}}
	b := &tinyProvider{name: "b", cap: resource.Resources{CPU: 1, Memory: resource.Giga}}
	m := MultiProvider(NamedProvider{Name: "a", Provider: a}, NamedProvider{Name: "b", Provider: b})

	if got := m.Capacity(); !got.Fits(resource.Resources{CPU: 2}) {
		t.Fatalf("capacity should sum, got %v", got)
	}
	if _, err := m.Provision(context.Background(), Spec{Resources: resource.Resources{CPU: 2}}); err != ErrNoMatch {
		t.Fatalf("fragmented capacity must not provision, got %v", err)
	}
}

func TestMultiDisposeRoutesToOwner(t *testing.T) {
	a := &tinyProvider{name: "a", cap: resource.Resources{CPU: 1, Memory: resource.Giga}}
	b := &tinyProvider{name: "b", cap: resource.Resources{CPU: 1, Memory: resource.Giga}}
	m := MultiProvider(NamedProvider{Name: "a", Provider: a}, NamedProvider{Name: "b", Provider: b})

	rt, _ := m.Provision(context.Background(), Spec{Resources: oneCPU})  // -> a
	rt2, _ := m.Provision(context.Background(), Spec{Resources: oneCPU}) // -> b

	if err := m.Dispose(context.Background(), rt2.ID); err != nil {
		t.Fatal(err)
	}
	if len(a.dispose) != 0 || len(b.dispose) != 1 {
		t.Fatalf("known id must route to owner only: a=%v b=%v", a.dispose, b.dispose)
	}
	if err := m.Dispose(context.Background(), rt.ID); err != nil {
		t.Fatal(err)
	}
	if len(a.dispose) != 1 {
		t.Fatalf("a dispose: %v", a.dispose)
	}
}

func TestMultiProviderPin(t *testing.T) {
	a := &tinyProvider{name: "a", cap: resource.Resources{CPU: 8, Memory: 8 * resource.Giga}}
	b := &tinyProvider{name: "b", cap: resource.Resources{CPU: 8, Memory: 8 * resource.Giga}}
	m := MultiProvider(NamedProvider{Name: "docker-a", Provider: a}, NamedProvider{Name: "k8s-b", Provider: b})

	// Pinned to the second provider even though the first fits.
	rt, err := m.Provision(context.Background(), Spec{Resources: oneCPU, Provider: "k8s-b"})
	if err != nil {
		t.Fatal(err)
	}
	if m.OwnerName(rt.ID) != "k8s-b" {
		t.Fatalf("pinned provision owner = %q", m.OwnerName(rt.ID))
	}

	// Unknown provider name never provisions.
	if _, err := m.Provision(context.Background(), Spec{Resources: oneCPU, Provider: "nope"}); err != ErrNoMatch {
		t.Fatalf("unknown pin: want ErrNoMatch, got %v", err)
	}

	// Claim records ownership for adopted runtimes.
	m.Claim("adopted-1", NamedProvider{Name: "docker-a", Provider: a})
	if m.OwnerName("adopted-1") != "docker-a" {
		t.Fatalf("claim owner = %q", m.OwnerName("adopted-1"))
	}
}
