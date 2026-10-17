// Package runtime models pooled buildkitd instances as first-class
// resources, independent of the docker/k8s supply layer.
package runtime

import (
	"sync"
	"time"

	"github.com/funcx27/kite/modules/buildfleet/resource"
)

type State string

const (
	Warm     State = "warm"
	Busy     State = "busy"
	Draining State = "draining"
	Failed   State = "failed"
)

type Spec struct {
	ID           string
	Resources    resource.Resources
	Platforms    []string
	Capabilities []string
	StartupCost  time.Duration
	// Provider pins the request to one named provider (config name);
	// empty = any provider, walking the preference order.
	Provider string
	// Affinity is a project key; builds with the same key prefer the
	// runtime that served them last (its local cache mounts hold the
	// project's dependency caches). Soft: mismatches fall back to the
	// regular pick.
	Affinity string
}

type Runtime struct {
	ID   string
	Spec Spec
	Addr string // buildkitd gRPC address
	// NodeName is the cluster node the runtime pod landed on, recorded by
	// the k8s provider at provision/adopt time; empty for node-less
	// providers (docker) or before placement.
	NodeName string
	State    State

	lastReleased time.Time
}

// Lease is an exclusive rental on a Runtime. Release returns it to the pool
// (warm); Fail marks it failed (not reusable without provider recovery).
// Both mutate Runtime.State under the pool mutex captured at Acquire time.
type Lease struct {
	rt  *Runtime
	mu  *sync.Mutex
	rel bool
}

func (l *Lease) Runtime() *Runtime { return l.rt }

func (l *Lease) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.rel {
		l.rel = true
		l.rt.State = Warm
		l.rt.lastReleased = time.Now()
	}
}

func (l *Lease) Fail() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.rel {
		l.rel = true
		l.rt.State = Failed
	}
}
