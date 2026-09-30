package fleet

import "github.com/funcx27/kite/modules/buildfleet/runtime"

// ErrNoCapacity is returned before any materialization when the pool has
// neither a matching warm runtime nor provider capacity for the request.
// It is the same sentinel the pool itself fails Acquire with.
var ErrNoCapacity = runtime.ErrNoMatch

// Error taxonomy per DESIGN.md §3: node-class failures fail over to a
// different runtime; resource-class back off; everything else is business.

// NodeError marks failures retryable on another runtime: runtime
// unreachable, context pull network failure, registry timeout.
type NodeError struct{ Err error }

func (e NodeError) Error() string { return e.Err.Error() }
func (e NodeError) Unwrap() error { return e.Err }

// ResourceError marks provision/scheduling failures: back off and retry,
// possibly at a lower profile.
type ResourceError struct{ Err error }

func (e ResourceError) Error() string { return e.Err.Error() }
func (e ResourceError) Unwrap() error { return e.Err }
