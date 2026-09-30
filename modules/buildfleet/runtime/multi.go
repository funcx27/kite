package runtime

import (
	"context"
	"sync"

	"github.com/funcx27/kite/modules/buildfleet/resource"
)

// Multi composes providers into one: capacity adds up, provisioning walks
// the list in order (preference) and picks the first provider whose
// remaining capacity fits the spec. Dispose routes IDs back to the owning
// provider.
type Multi struct {
	mu    sync.Mutex
	subs  []NamedProvider
	owner map[string]NamedProvider
}

// NamedProvider pairs a provider with its config name ("docker-1",
// "prod-k8s") for status and logging.
type NamedProvider struct {
	Name     string
	Provider Provider
}

func MultiProvider(ps ...NamedProvider) *Multi {
	return &Multi{subs: ps, owner: map[string]NamedProvider{}}
}

// Owner returns the provider that provisioned id (zero value if unknown).
func (m *Multi) Owner(id string) Provider {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.owner[id].Provider
}

// OwnerName returns the config name of the provider that provisioned id.
func (m *Multi) OwnerName(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.owner[id].Name
}

// Claim records ownership of an adopted runtime (reconcile paths that
// register instances the pool did not provision).
func (m *Multi) Claim(id string, np NamedProvider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.owner[id] = np
}

func (m *Multi) Capacity() resource.Resources {
	total := resource.Resources{}
	for _, np := range m.subs {
		total = total.Add(np.Provider.Capacity())
	}
	return total
}

func (m *Multi) Provision(ctx context.Context, spec Spec) (*Runtime, error) {
	for _, np := range m.subs {
		if spec.Provider != "" && np.Name != spec.Provider {
			continue
		}
		if !np.Provider.Capacity().Fits(spec.Resources) {
			continue
		}
		rt, err := np.Provider.Provision(ctx, spec)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		m.owner[rt.ID] = np
		m.mu.Unlock()
		return rt, nil
	}
	// Aggregate capacity may fit while no single provider does
	// (fragmentation); surface as no-match so the pool fails fast.
	return nil, ErrNoMatch
}

func (m *Multi) Dispose(ctx context.Context, id string) error {
	m.mu.Lock()
	p, ok := m.owner[id]
	if ok {
		delete(m.owner, id)
	}
	subs := m.subs
	m.mu.Unlock()

	if ok {
		return p.Provider.Dispose(ctx, id)
	}
	// Unknown ID (e.g. daemon restart): try each provider; NotFound is
	// swallowed by each Dispose, so any error here is real.
	for _, np := range subs {
		if err := np.Provider.Dispose(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

var _ Provider = (*Multi)(nil)
