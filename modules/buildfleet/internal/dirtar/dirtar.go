// Package dirtar writes a deterministic tarball of a directory: zero
// ModTime, no device/special entries, so identical trees yield identical
// bytes (and thus identical store digests). Shared by the git and http
// context materializers.
package dirtar

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Write tars root (recursively) into a new temp file and returns its path;
// the caller removes it. skip lists directory-entry names excluded from the
// walk (e.g. ".git" for clone trees); symlinks and other non-regular files
// are skipped for v1, mirroring the context semantics buildfleet promises.
func Write(root string, skip ...string) (string, error) {
	f, err := os.CreateTemp("", "buildfleet-dirtar-*.tar")
	if err != nil {
		return "", fmt.Errorf("dirtar: temp tar: %w", err)
	}
	skipped := make(map[string]bool, len(skip))
	for _, s := range skip {
		skipped[s] = true
	}
	tw := tar.NewWriter(f)
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		base := filepath.Base(path)
		if skipped[base] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		hdr := &tar.Header{
			Name:    name,
			Mode:    int64(info.Mode().Perm()),
			ModTime: time.Time{}, // deterministic: same tree -> same bytes
		}
		switch {
		case d.IsDir():
			hdr.Typeflag = tar.TypeDir
			return tw.WriteHeader(hdr)
		case info.Mode().IsRegular():
			hdr.Typeflag = tar.TypeReg
			hdr.Size = info.Size()
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			src, err := os.Open(path)
			if err != nil {
				return err
			}
			defer src.Close()
			_, err = io.Copy(tw, src)
			return err
		default:
			return nil // skip symlinks etc. for v1
		}
	})
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("dirtar: tar: %w", err)
	}
	if err := tw.Close(); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("dirtar: tar close: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("dirtar: tar file close: %w", err)
	}
	return f.Name(), nil
}
