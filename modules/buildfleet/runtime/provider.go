package runtime

import (
	"context"

	"github.com/funcx27/kite/modules/buildfleet/resource"
)

// Provider supplies runtimes. Dispose must be idempotent (return nil when
// the instance no longer exists): both failover and restart-reconcile call it.
type Provider interface {
	Provision(ctx context.Context, spec Spec) (*Runtime, error)
	Dispose(ctx context.Context, id string) error
	Capacity() resource.Resources
}
