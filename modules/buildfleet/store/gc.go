package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GC reclaims blobs older than TTL unless referenced (refcount > 0).
type GC struct {
	store *LocalStore
	ttl   time.Duration
	now   func() time.Time
}

func NewGC(s *LocalStore, ttl time.Duration) *GC {
	return &GC{store: s, ttl: ttl, now: time.Now}
}

func (g *GC) SetNow(fn func() time.Time) { g.now = fn }

func (g *GC) Run(ctx context.Context) error {
	cutoff := g.now().Add(-g.ttl)
	return filepath.WalkDir(g.store.blobDir(), func(path string, d os.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			// A blob removed between readdir and visit is already
			// collected; only that entry is gone.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.ModTime().After(cutoff) {
			return nil
		}
		digest := digestFromPath(g.store.blobDir(), path)
		if g.store.refCount(digest) > 0 {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	})
}

// digestFromPath reconstructs "sha256:<hex>" from blobs/<aa>/<rest>.
func digestFromPath(blobDir, path string) string {
	rel, err := filepath.Rel(blobDir, path)
	if err != nil {
		return ""
	}
	_, sum, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return "sha256:" + sum
}
