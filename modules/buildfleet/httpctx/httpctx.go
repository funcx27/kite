// Package httpctx turns an HTTP(S) URL into a deterministic build-context
// tarball. Downloads happen caller-side (same contract as gitctx,
// DESIGN.md §6): the runtime needs no egress to the file server.
package httpctx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/funcx27/kite/modules/buildfleet/internal/dirtar"
)

// Source describes one HTTP file build context.
type Source struct {
	URL      string // download URL (http/https)
	Mode     string // "server" (default, daemon-side fetch) | "runtime" (buildkitd fetches)
	AuthEnv  string // env var holding "user:token" for basic auth
	Username string // literal basic auth (API passthrough); used when AuthEnv is empty
	Password string
}

// Result is the materialized context tarball.
type Result struct {
	TarPath string // temp file; caller must remove
}

// Fetch downloads src and tars it into a deterministic context (zero
// ModTime, regular files and dirs only — same guarantees as gitctx), so
// the same bytes at the URL always yield the same store digest. A
// (possibly gzipped) tar is extracted into the context root; any other
// body becomes a single-file context named after the URL's last path
// segment, stored as downloaded.
func Fetch(ctx context.Context, src Source) (*Result, error) {
	u, err := url.Parse(src.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("httpctx: url must be http(s), got %q", src.URL)
	}
	user, pass, err := basicAuth(src)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("httpctx: url: %w", err)
	}
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("httpctx: download %s: %w", src.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("httpctx: download %s: status %s", src.URL, resp.Status)
	}
	body, err := os.CreateTemp("", "buildfleet-httpctx-*")
	if err != nil {
		return nil, fmt.Errorf("httpctx: temp file: %w", err)
	}
	defer os.Remove(body.Name())
	if _, err := io.Copy(body, resp.Body); err != nil {
		body.Close()
		return nil, fmt.Errorf("httpctx: download %s: %w", src.URL, err)
	}
	if err := body.Close(); err != nil {
		return nil, fmt.Errorf("httpctx: temp file close: %w", err)
	}

	dir, err := os.MkdirTemp("", "buildfleet-httpdir-*")
	if err != nil {
		return nil, fmt.Errorf("httpctx: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := materialize(body.Name(), dir, src.URL); err != nil {
		return nil, err
	}
	tarPath, err := dirtar.Write(dir)
	if err != nil {
		return nil, err
	}
	return &Result{TarPath: tarPath}, nil
}

// basicAuth resolves download credentials with the same precedence as
// gitctx: AuthEnv ("user:token"), then literal Username/Password.
func basicAuth(src Source) (user, pass string, err error) {
	switch {
	case src.AuthEnv != "":
		v, ok := os.LookupEnv(src.AuthEnv)
		if !ok {
			return "", "", fmt.Errorf("httpctx: env %s not set", src.AuthEnv)
		}
		user, pass, _ = strings.Cut(v, ":")
	case src.Username != "":
		user, pass = src.Username, src.Password
	}
	return user, pass, nil
}
