package httpctx

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// materialize unpacks the downloaded body into dir: a tar body is
// extracted (entries escaping dir are rejected), anything else becomes a
// single file named after the URL's last path segment.
func materialize(body, dir, rawURL string) error {
	f, err := os.Open(body)
	if err != nil {
		return fmt.Errorf("httpctx: open body: %w", err)
	}
	defer f.Close()
	tarLike, err := isTar(f)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("httpctx: rewind body: %w", err)
	}
	if !tarLike {
		return writeSingle(f, dir, urlFileName(rawURL))
	}
	return untar(f, dir)
}

// bodyReader wraps f, transparently gunzipping when the gzip magic leads
// the file. The magic peek rewinds, so the returned reader starts at the
// top of the body either way.
func bodyReader(f *os.File) (io.Reader, func(), error) {
	var magic [2]byte
	n, _ := io.ReadFull(f, magic[:])
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, nil, fmt.Errorf("httpctx: rewind body: %w", err)
	}
	if n == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, nil, fmt.Errorf("httpctx: gunzip: %w", err)
		}
		return gz, func() { gz.Close() }, nil
	}
	return f, func() {}, nil
}

// isTar reports whether the (gunzipped) body starts with a parseable tar
// header.
func isTar(f *os.File) (bool, error) {
	r, closeR, err := bodyReader(f)
	if err != nil {
		return false, err
	}
	defer closeR()
	_, err = tar.NewReader(r).Next()
	return err == nil, nil
}

func untar(f *os.File, dir string) error {
	r, closeR, err := bodyReader(f)
	if err != nil {
		return err
	}
	defer closeR()
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("httpctx: untar: %w", err)
		}
		target := safeTarget(dir, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("httpctx: untar %s: %w", hdr.Name, err)
			}
		case tar.TypeReg:
			mode := hdr.FileInfo().Mode().Perm()
			if mode == 0 {
				mode = 0o644
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("httpctx: untar %s: %w", hdr.Name, err)
			}
			dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return fmt.Errorf("httpctx: untar %s: %w", hdr.Name, err)
			}
			if _, err := io.Copy(dst, tr); err != nil {
				dst.Close()
				return fmt.Errorf("httpctx: untar %s: %w", hdr.Name, err)
			}
			if err := dst.Close(); err != nil {
				return fmt.Errorf("httpctx: untar %s: %w", hdr.Name, err)
			}
		default:
			// symlinks etc. skipped for v1, mirroring dirtar semantics
		}
	}
}

// safeTarget maps a tar entry name to a path inside dir. The "/"+name
// Clean forces every name to root inside dir, so absolute paths and
// dot-dot segments can never escape it.
func safeTarget(dir, name string) string {
	clean := filepath.Clean("/" + strings.TrimPrefix(filepath.ToSlash(name), "/"))
	return filepath.Join(dir, clean)
}

func writeSingle(body io.Reader, dir, name string) error {
	dst, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("httpctx: single file %s: %w", name, err)
	}
	defer dst.Close()
	if _, err := io.Copy(dst, body); err != nil {
		return fmt.Errorf("httpctx: single file %s: %w", name, err)
	}
	return nil
}

// urlFileName derives the single-file context name from the URL path,
// falling back to "download" for empty or trailing-slash paths.
func urlFileName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "download"
	}
	name := path.Base(u.Path)
	if name == "" || name == "." || name == "/" {
		return "download"
	}
	return name
}
