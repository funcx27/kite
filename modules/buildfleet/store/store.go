// Package store provides content-addressable artifact storage for build
// contexts and logs. Digests are references exchanged between caller and
// runtime; big payloads never stream through the scheduler (DESIGN.md §6).
package store

import (
	"context"
	"io"
)

type Store interface {
	Push(ctx context.Context, r io.Reader) (digest string, err error)
	Pull(ctx context.Context, digest string) (io.ReadCloser, error)
	PullToFile(ctx context.Context, digest, path string) error
	// PullToDir untars a digest into dir (created if missing); the layout
	// is the build context the builder streams to the runtime.
	PullToDir(ctx context.Context, digest, dir string) error
	// AcquireRef increments the GC protection refcount for digest and
	// returns its release func. Refcounts persist alongside the store.
	AcquireRef(digest string) (release func(), err error)
}
