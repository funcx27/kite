package store

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LocalStore is a filesystem-backed Store: blobs under <root>/blobs/<aa>/<rest>,
// refcounts under <root>/refs/<digest>.json so they survive restarts.
type LocalStore struct {
	root string
	mu   sync.Mutex
}

func NewLocalStore(root string) *LocalStore {
	return &LocalStore{root: root}
}

func (s *LocalStore) Push(ctx context.Context, r io.Reader) (string, error) {
	if err := os.MkdirAll(s.blobDir(), 0o755); err != nil {
		return "", fmt.Errorf("localstore push: mkdir blobs: %w", err)
	}
	tmp, err := os.CreateTemp(s.blobDir(), "ingest-*")
	if err != nil {
		return "", fmt.Errorf("localstore push: create tmp: %w", err)
	}
	defer os.Remove(tmp.Name())

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		tmp.Close()
		return "", fmt.Errorf("localstore push: copy: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("localstore push: close: %w", err)
	}
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	dst := s.path(digest)
	if _, err := os.Stat(dst); err == nil {
		return digest, nil // dedup
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("localstore push: mkdir: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", fmt.Errorf("localstore push: rename: %w", err)
	}
	return digest, nil
}

func (s *LocalStore) Pull(ctx context.Context, digest string) (io.ReadCloser, error) {
	f, err := os.Open(s.path(digest))
	if err != nil {
		return nil, fmt.Errorf("localstore pull: %w", err)
	}
	return f, nil
}

func (s *LocalStore) PullToFile(ctx context.Context, digest, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("localstore pulltofile: create: %w", err)
	}
	defer f.Close()
	src, err := s.Pull(ctx, digest)
	if err != nil {
		return err
	}
	defer src.Close()
	if _, err := io.Copy(f, src); err != nil {
		return fmt.Errorf("localstore pulltofile: copy: %w", err)
	}
	return nil
}

// PullToDir untars the blob into dir. Dir entries resolve inside dir
// (Join cleans the path); entries escaping via ".." are rejected.
func (s *LocalStore) PullToDir(ctx context.Context, digest, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("localstore pulltodir: mkdir: %w", err)
	}
	src, err := s.Pull(ctx, digest)
	if err != nil {
		return err
	}
	defer src.Close()
	tr := tar.NewReader(src)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("localstore pulltodir: read tar: %w", err)
		}
		name := filepath.Clean(hdr.Name)
		if filepath.IsAbs(name) || strings.HasPrefix(name, "..") {
			return fmt.Errorf("localstore pulltodir: entry escapes dir: %q", hdr.Name)
		}
		dst := filepath.Join(dir, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return fmt.Errorf("localstore pulltodir: mkdir %s: %w", name, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return fmt.Errorf("localstore pulltodir: mkdir %s: %w", name, err)
			}
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return fmt.Errorf("localstore pulltodir: create %s: %w", name, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("localstore pulltodir: copy %s: %w", name, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("localstore pulltodir: close %s: %w", name, err)
			}
		default:
			return fmt.Errorf("localstore pulltodir: unsupported entry type %q in %s", hdr.Typeflag, name)
		}
	}
}

// AcquireRef persists a refcount bump so restart does not lose in-flight
// build protection (DESIGN.md §5.1).
func (s *LocalStore) AcquireRef(digest string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.bumpRef(digest, +1); err != nil {
		return nil, err
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		// best-effort: release has no error channel, and -1 clamps at 0
		_ = s.bumpRef(digest, -1)
	}, nil
}

type refFile struct {
	Count   int       `json:"count"`
	Updated time.Time `json:"updated"`
}

func (s *LocalStore) bumpRef(digest string, d int) error {
	rf := refFile{}
	p := s.refPath(digest)
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, &rf); err != nil {
			return fmt.Errorf("localstore refcount read: %w", err)
		}
	}
	rf.Count += d
	if rf.Count < 0 {
		rf.Count = 0
	}
	rf.Updated = time.Now()
	if rf.Count == 0 {
		return os.Remove(p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("localstore refcount mkdir: %w", err)
	}
	b, err := json.Marshal(rf)
	if err != nil {
		return fmt.Errorf("localstore refcount marshal: %w", err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return fmt.Errorf("localstore refcount write: %w", err)
	}
	return nil
}

func (s *LocalStore) refCount(digest string) int {
	b, err := os.ReadFile(s.refPath(digest))
	if err != nil {
		return 0
	}
	rf := refFile{}
	if err := json.Unmarshal(b, &rf); err != nil {
		return 0
	}
	return rf.Count
}

func (s *LocalStore) blobDir() string { return filepath.Join(s.root, "blobs") }

func (s *LocalStore) path(digest string) string {
	sum := digest
	if i := strings.IndexByte(digest, ':'); i >= 0 {
		sum = digest[i+1:]
	}
	return filepath.Join(s.blobDir(), sum[:2], sum)
}

func (s *LocalStore) refPath(digest string) string {
	return filepath.Join(s.root, "refs", digest+".json")
}
