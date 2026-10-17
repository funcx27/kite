// Package builder drives buildkit Solve against a pooled runtime.
// Credentials attach per-Solve via the session; the runtime instance holds
// none (DESIGN.md §7.1).
package builder

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/tonistiigi/fsutil"

	"github.com/funcx27/kite/modules/buildfleet/creds"
)

type SolveInput struct {
	ContextDigest string // ctx://<digest> from the Store (caller resolves to ContextDir)
	ContextDir    string // local directory the runtime can see
	// GitURL is a normalized remote git URL (with #ref:subdir fragment).
	// When set, buildkitd fetches the repo itself (no LocalMounts) and
	// git credentials ride the secrets session.
	GitURL string
	// HTTPURL is a remote http(s) context URL. When set, buildkitd
	// downloads it itself (tarball semantics) instead of using
	// LocalMounts; auth rides HTTP_AUTH_* secrets on the session.
	HTTPURL    string
	Dockerfile string
	// DockerfileContent, when set, overrides the Dockerfile entirely:
	// it rides the "dockerfile" local mount and the context is never
	// consulted for it (works with remote git contexts).
	DockerfileContent string
	Args              map[string]string
	Platforms         []string
	OutputRef         string // image ref to push, or "" for no push
	// TarOut replaces the push export with a tar export written to this
	// path: probe solves fetch context file content without a registry.
	// Mutually exclusive with OutputRef in practice (a probe never pushes).
	TarOut string
	Creds  creds.Table
	// CacheRef enables registry layer-cache round-trip (import at solve
	// start, export on completion); empty disables both.
	CacheRef string
	// GitSecrets maps git host -> auth for the secrets session:
	// GIT_AUTH_HEADER.<host> (raw Authorization value) or
	// GIT_AUTH_TOKEN.<host> (token, wrapped as x-access-token by buildkitd).
	GitSecrets map[string]SourceSecret
	// HTTPSecrets maps http source host -> auth for the secrets session:
	// HTTP_AUTH_HEADER_<hostname> (raw Authorization value) or
	// HTTP_AUTH_TOKEN_<hostname> (token, wrapped as Bearer by buildkitd).
	// The key's port is stripped — buildkitd matches by u.Hostname().
	HTTPSecrets map[string]SourceSecret
}

// SourceSecret is one host's remote-source auth for the secrets session.
type SourceSecret struct {
	Header string // full Authorization header value, e.g. "Basic ..."
	Token  string // bare token; buildkitd wraps it (x-access-token / Bearer)
}

type Result struct {
	Digest   string
	ImageRef string
}

type Builder struct{}

func New() *Builder { return &Builder{} }

// Solve connects to the buildkitd at addr and runs a dockerfile build.
// addr is a bare host:port or a grpc/tcp URL. status (optional) receives
// raw progress; use Watch to decode it.
func (b *Builder) Solve(ctx context.Context, addr string, in SolveInput, status chan *client.SolveStatus) (*Result, error) {
	if !strings.Contains(addr, "://") {
		addr = "tcp://" + addr
	}
	c, err := client.New(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("builder: connect %s: %w", addr, err)
	}
	defer c.Close()

	// Preflight: grpc connect backoff would burn ~15s against a dead
	// runtime before surfacing Unavailable; fail fast instead.
	if err := pingReady(ctx, c); err != nil {
		return nil, fmt.Errorf("builder: preflight %s: %w", addr, err)
	}

	opts, err := b.solveOpt(ctx, in)
	if err != nil {
		return nil, err
	}
	resp, err := c.Solve(ctx, nil, *opts, status)
	if err != nil {
		return nil, fmt.Errorf("builder: solve: %w", err)
	}
	return &Result{
		Digest:   resp.ExporterResponse["containerimage.digest"],
		ImageRef: in.OutputRef,
	}, nil
}

func (b *Builder) solveOpt(ctx context.Context, in SolveInput) (*client.SolveOpt, error) {
	attrs := map[string]string{"filename": in.Dockerfile}
	for k, v := range in.Args {
		attrs["build-arg:"+k] = v
	}
	if len(in.Platforms) > 0 {
		attrs["platform"] = strings.Join(in.Platforms, ",")
	}

	opts := &client.SolveOpt{
		Frontend:      "dockerfile.v0",
		FrontendAttrs: attrs,
		Exports:       exports(in),
		// Ref must be unique per solve: a warm runtime rejects a reused
		// job ID ("job ID ... exists").
		Ref: "buildfleet-" + rand.Text(),
	}
	if in.CacheRef != "" {
		// Import is best-effort (a missing ref only warns); mode=max
		// exports intermediate layers, not just the final ones.
		opts.CacheImports = []client.CacheOptionsEntry{{
			Type: "registry", Attrs: map[string]string{"ref": in.CacheRef},
		}}
		opts.CacheExports = []client.CacheOptionsEntry{{
			Type: "registry", Attrs: map[string]string{"ref": in.CacheRef, "mode": "max"},
		}}
	}

	if in.DockerfileContent != "" {
		// External Dockerfile with a git context needs the frontend's
		// "force local dockerfile" escape hatch: dockerfilekey flips
		// ReadEntrypoint off the git-source file, and
		// local-sessionid:dockerfile routes the read to the shared
		// session, whose filesync provider serves the content.
		dir, err := os.MkdirTemp("", "buildfleet-dockerfile-*")
		if err != nil {
			return nil, fmt.Errorf("builder: dockerfile temp dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(in.DockerfileContent), 0o644); err != nil {
			return nil, fmt.Errorf("builder: write dockerfile: %w", err)
		}
		dfs, err := fsutil.NewFS(dir)
		if err != nil {
			return nil, fmt.Errorf("builder: dockerfile fs: %w", err)
		}
		s, err := session.NewSession(ctx, "buildfleet")
		if err != nil {
			return nil, fmt.Errorf("builder: session: %w", err)
		}
		s.Allow(filesync.NewFSSyncProvider(filesync.StaticDirSource{
			"dockerfile": dfs,
		}))
		opts.SharedSession = s
		// dockerfilekey's value is the local mount NAME it forces;
		// local-sessionid:<name> routes the read to our session. The
		// filename attr must be the fixed name served by the mount —
		// the request's Dockerfile field may carry the content itself.
		attrs["filename"] = "Dockerfile"
		attrs["dockerfilekey"] = "dockerfile"
		attrs["local-sessionid:dockerfile"] = s.ID()
	}

	if in.GitURL != "" || in.HTTPURL != "" {
		// Remote context: buildkitd's source handler fetches it (git exec
		// for git URLs, HTTP download otherwise) and reads the Dockerfile
		// from it.
		if in.GitURL != "" {
			opts.FrontendAttrs["context"] = in.GitURL
		} else {
			opts.FrontendAttrs["context"] = in.HTTPURL
		}
		opts.Session = credsSession(in)
		return opts, nil
	}

	fs, err := fsutil.NewFS(in.ContextDir)
	if err != nil {
		return nil, fmt.Errorf("builder: context dir %s: %w", in.ContextDir, err)
	}
	// dockerfile.v0 reads the Dockerfile through its own mount, which
	// buildx points at the file's directory; our context is one flat dir.
	if _, ok := opts.LocalMounts["dockerfile"]; !ok {
		opts.LocalMounts = map[string]fsutil.FS{"context": fs, "dockerfile": fs}
	} else {
		opts.LocalMounts["context"] = fs
	}
	opts.Session = credsSession(in)
	return opts, nil
}

// Event is one decoded progress item from a SolveStatus batch.
type Event struct {
	Vertex *client.Vertex
	Log    *client.VertexLog
	// Start announces the acquired runtime right before Solve begins.
	Start *StartEvent
}

// StartEvent reports which runtime (and owning provider) a build will
// run on, emitted once per attempt.
type StartEvent struct {
	RuntimeID string
	Provider  string
	Addr      string
	Attempt   int
}

// Watch drains a solve status channel, forwarding vertex/log events to fn.
// It returns when ch is closed or fn returns false.
func Watch(ch <-chan *client.SolveStatus, fn func(Event) bool) error {
	for st := range ch {
		for _, v := range st.Vertexes {
			if !fn(Event{Vertex: v}) {
				return nil
			}
		}
		for _, l := range st.Logs {
			if !fn(Event{Log: l}) {
				return nil
			}
		}
	}
	return nil
}

// ErrUnreachable marks a runtime that failed the preflight ping; the
// facade treats it as node-class and fails over.
var ErrUnreachable = errors.New("runtime unreachable")

// Alive reports whether the buildkitd at addr answers an Info call
// within 3s. The fleet uses it to distinguish a dead runtime (failover,
// dispose) from a transient transport failure (retry on the same one).
func Alive(ctx context.Context, addr string) bool {
	if !strings.Contains(addr, "://") {
		addr = "tcp://" + addr
	}
	c, err := client.New(ctx, addr)
	if err != nil {
		return false
	}
	defer c.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = c.Info(pingCtx)
	return err == nil
}

func pingReady(ctx context.Context, c *client.Client) error {
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := c.Info(pingCtx); err != nil {
		return fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	return nil
}

// exports picks the solve's export: a tar file when TarOut is set (probe),
// an image push when OutputRef is set, otherwise nothing.
func exports(in SolveInput) []client.ExportEntry {
	if in.TarOut != "" {
		return []client.ExportEntry{{
			Type: client.ExporterTar,
			Output: func(map[string]string) (io.WriteCloser, error) {
				f, err := os.Create(in.TarOut)
				if err != nil {
					return nil, fmt.Errorf("create tar export: %w", err)
				}
				return f, nil
			},
		}}
	}
	return pushExports(in.OutputRef)
}

func pushExports(outputRef string) []client.ExportEntry {
	if outputRef == "" {
		return nil
	}
	return []client.ExportEntry{{
		Type: client.ExporterImage,
		Attrs: map[string]string{
			"name": outputRef,
			"push": "true",
		},
	}}
}
