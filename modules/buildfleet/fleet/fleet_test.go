package fleet

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/moby/buildkit/client"
	grpccodes "google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/funcx27/kite/modules/buildfleet/builder"
	"github.com/funcx27/kite/modules/buildfleet/creds"
	"github.com/funcx27/kite/modules/buildfleet/gitctx"
	"github.com/funcx27/kite/modules/buildfleet/httpctx"
	"github.com/funcx27/kite/modules/buildfleet/resource"
	"github.com/funcx27/kite/modules/buildfleet/runtime"
	"github.com/funcx27/kite/modules/buildfleet/store"
)

type fakeBuilder struct {
	failures    int // fail this many times with node-class error
	calls       int
	lastInput   builder.SolveInput
	failNext    func() error // optional per-call failure
	slowDigest  string       // non-empty: sleep, then return this digest
	digestCount *int32       // when slowDigest set, counts invocations
}

func (f *fakeBuilder) Solve(ctx context.Context, addr string, in builder.SolveInput, status chan *client.SolveStatus) (*builder.Result, error) {
	f.calls++
	f.lastInput = in
	if fn := f.failNext; fn != nil {
		defer func() { f.failNext = nil }()
		return nil, fn()
	}
	if f.slowDigest != "" {
		if f.digestCount != nil {
			atomic.AddInt32(f.digestCount, 1)
		}
		time.Sleep(200 * time.Millisecond)
		return &builder.Result{Digest: f.slowDigest}, nil
	}
	if f.calls <= f.failures {
		return nil, NodeError{Err: errors.New("node down")}
	}
	return &builder.Result{Digest: "sha256:ok"}, nil
}

type alwaysBusiness struct{}

func (alwaysBusiness) Solve(ctx context.Context, addr string, in builder.SolveInput, status chan *client.SolveStatus) (*builder.Result, error) {
	return nil, errors.New("Dockerfile parse error")
}

type countingProvider struct {
	got     int
	used    resource.Resources
	zeroCap bool
}

func (p *countingProvider) Provision(ctx context.Context, spec runtime.Spec) (*runtime.Runtime, error) {
	p.got++
	p.used = p.used.Add(spec.Resources)
	return &runtime.Runtime{ID: spec.ID, Spec: spec, Addr: "127.0.0.1:1", State: runtime.Warm}, nil
}

func (p *countingProvider) Dispose(ctx context.Context, id string) error { return nil }

func (p *countingProvider) Capacity() resource.Resources {
	if p.zeroCap {
		return resource.Resources{}
	}
	return resource.Resources{CPU: 64, Memory: 128 * resource.Giga}.Sub(p.used)
}

func newCapableFakeProvider() *countingProvider { return &countingProvider{} }

func TestRetryOnNodeClass(t *testing.T) {
	prov := newCapableFakeProvider()
	pool := runtime.NewPool(prov)
	fb := &fakeBuilder{failures: 2}
	f := New(pool, fb)

	res, err := f.Build(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Digest != "sha256:ok" {
		t.Fatalf("got %+v", res)
	}
	if fb.calls != 3 {
		t.Fatalf("want 3 attempts got %d", fb.calls)
	}
	if res.Attempts != 3 {
		t.Fatalf("want Attempts=3 got %d", res.Attempts)
	}
	if prov.got != 3 {
		t.Fatalf("each retry must fail over to a fresh runtime, provisioned %d", prov.got)
	}
}

func TestNoRetryOnBusinessError(t *testing.T) {
	pool := runtime.NewPool(newCapableFakeProvider())
	f := New(pool, &alwaysBusiness{})
	if _, err := f.Build(context.Background(), Request{}); err == nil {
		t.Fatal("business error must surface")
	}
}

func TestExhaustedRetries(t *testing.T) {
	pool := runtime.NewPool(newCapableFakeProvider())
	fb := &fakeBuilder{failures: 100}
	f := New(pool, fb, WithMaxRetry(2))
	_, err := f.Build(context.Background(), Request{})
	if err == nil {
		t.Fatal("exhausted node failures must surface")
	}
	var ne NodeError
	if !errors.As(err, &ne) {
		t.Fatalf("last node error must be wrapped, got %v", err)
	}
	if fb.calls != 2 {
		t.Fatalf("maxRetry bounds attempts: want 2 got %d", fb.calls)
	}
}

func TestContextCancellation(t *testing.T) {
	pool := runtime.NewPool(newCapableFakeProvider())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := New(pool, &fakeBuilder{failures: 1})
	if _, err := f.Build(ctx, Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled got %v", err)
	}
}

func TestRequestAdapters(t *testing.T) {
	req := Request{
		ContextDigest: "sha256:abc",
		Dockerfile:    "Dockerfile",
		Args:          map[string]string{"A": "1"},
		Platforms:     []string{"linux/amd64"},
		OutputRef:     "reg.example.com/img:1",
		Resources:     resource.Resources{CPU: 2, Memory: 4 * resource.Giga},
		StartupCost:   time.Second,
	}
	spec := req.spec()
	if spec.Resources != req.Resources || spec.Platforms[0] != "linux/amd64" || spec.StartupCost != time.Second {
		t.Fatalf("spec adapter mismatch: %+v", spec)
	}
	in := req.input(&buildConfig{})
	if in.ContextDigest != req.ContextDigest || in.Dockerfile != req.Dockerfile ||
		in.Args["A"] != "1" || in.OutputRef != req.OutputRef {
		t.Fatalf("input adapter mismatch: %+v", in)
	}
}

func TestBuildRuntimeGitMode(t *testing.T) {
	solver := &fakeBuilder{}
	// no WithStore: runtime mode must not need one
	f := New(runtime.NewPool(&countingProvider{}), solver)

	t.Setenv("BF_GIT_AUTH", "user:pass")
	res, err := f.Build(context.Background(), Request{
		Git: &gitctx.Source{
			URL:     "https://git.example.com/team/app",
			Mode:    "runtime",
			Ref:     "v1",
			Subdir:  "app",
			AuthEnv: "BF_GIT_AUTH",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Digest != "sha256:ok" {
		t.Fatalf("digest: %q", res.Digest)
	}
	got := solver.lastInput
	if got.GitURL != "https://git.example.com/team/app.git#v1:app" {
		t.Fatalf("git url: %q", got.GitURL)
	}
	sec, ok := got.GitSecrets["git.example.com"]
	if !ok || sec.Header != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:pass")) {
		t.Fatalf("git secrets: %+v", got.GitSecrets)
	}
	if got.ContextDir != "" || got.ContextDigest != "" {
		t.Fatalf("runtime mode must not materialize context: %+v", got)
	}
}

func TestBuildRuntimeGitModeRejectsLocalPath(t *testing.T) {
	f := New(runtime.NewPool(&countingProvider{}), &fakeBuilder{})
	if _, err := f.Build(context.Background(), Request{
		Git: &gitctx.Source{URL: "/tmp/repo", Mode: "runtime"},
	}); err == nil {
		t.Fatal("local path must be rejected in runtime mode")
	}
}

// httpFileServer serves body at /ctx.tar, requiring basic auth when
// userpass is non-empty.
func httpFileServer(t *testing.T, body []byte, userpass string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if userpass != "" && r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(userpass)) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write(body)
	}))
}

// tarOf builds an in-memory tar from name -> content entries.
func tarOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(content)), Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// probingSolver records a context file's content at solve time, before
// the fleet cleans the materialized dir up.
type probingSolver struct {
	fakeBuilder
	gotFile string
}

func (p *probingSolver) Solve(ctx context.Context, addr string, in builder.SolveInput, status chan *client.SolveStatus) (*builder.Result, error) {
	b, err := os.ReadFile(filepath.Join(in.ContextDir, "hello.txt"))
	if err == nil {
		p.gotFile = string(b)
	}
	return p.fakeBuilder.Solve(ctx, addr, in, status)
}

func TestBuildServerHTTPMode(t *testing.T) {
	srv := httpFileServer(t, tarOf(t, map[string]string{"Dockerfile": "FROM scratch", "hello.txt": "hi"}), "")
	defer srv.Close()
	solver := &probingSolver{}
	f := New(runtime.NewPool(&countingProvider{}), solver, WithStore(store.NewLocalStore(t.TempDir())))

	res, err := f.Build(context.Background(), Request{HTTP: &httpctx.Source{URL: srv.URL + "/ctx.tar"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Digest != "sha256:ok" {
		t.Fatalf("digest: %q", res.Digest)
	}
	in := solver.lastInput
	if !strings.HasPrefix(in.ContextDigest, "ctx://sha256:") {
		t.Fatalf("http context not normalized to digest: %q", in.ContextDigest)
	}
	if solver.gotFile != "hi" {
		t.Fatalf("materialized context content: %q", solver.gotFile)
	}
	if in.HTTPURL != "" || in.GitURL != "" {
		t.Fatalf("server mode must not set a remote context: %+v", in)
	}
}

func TestBuildServerHTTPModeUsesGitKindCreds(t *testing.T) {
	srv := httpFileServer(t, tarOf(t, map[string]string{"Dockerfile": "FROM scratch"}), "user:pass")
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	solver := &fakeBuilder{}
	f := New(runtime.NewPool(&countingProvider{}), solver, WithStore(store.NewLocalStore(t.TempDir())))

	_, err := f.Build(context.Background(), Request{
		HTTP: &httpctx.Source{URL: srv.URL + "/ctx.tar"},
		Creds: creds.Table{{
			Kind:   creds.Git,
			Match:  u.Host,
			Direct: &creds.Secret{Username: "user", Secret: "pass"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(solver.lastInput.ContextDigest, "ctx://sha256:") {
		t.Fatalf("download with kind-git credential did not materialize: %q", solver.lastInput.ContextDigest)
	}
}

func TestBuildRuntimeHTTPMode(t *testing.T) {
	solver := &fakeBuilder{}
	// no WithStore: runtime mode must not need one (and never contacts
	// the URL daemon-side).
	f := New(runtime.NewPool(&countingProvider{}), solver)
	url := "https://files.example.com/team/app-1.0.tar.gz"

	t.Setenv("BF_HTTP_AUTH", "user:pass")
	res, err := f.Build(context.Background(), Request{
		HTTP: &httpctx.Source{URL: url, Mode: "runtime", AuthEnv: "BF_HTTP_AUTH"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Digest != "sha256:ok" {
		t.Fatalf("digest: %q", res.Digest)
	}
	got := solver.lastInput
	if got.HTTPURL != url {
		t.Fatalf("http url: %q", got.HTTPURL)
	}
	sec, ok := got.HTTPSecrets["files.example.com"]
	if !ok || sec.Header != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:pass")) {
		t.Fatalf("http secrets: %+v", got.HTTPSecrets)
	}
	if got.ContextDigest != "" || got.ContextDir != "" {
		t.Fatalf("runtime mode must not materialize context: %+v", got)
	}
}

func TestBuildRuntimeHTTPModeRejectsNonHTTP(t *testing.T) {
	f := New(runtime.NewPool(&countingProvider{}), &fakeBuilder{})
	if _, err := f.Build(context.Background(), Request{
		HTTP: &httpctx.Source{URL: "/tmp/ctx.tar", Mode: "runtime"},
	}); err == nil {
		t.Fatal("local path must be rejected in runtime mode")
	}
	if _, err := f.Build(context.Background(), Request{
		HTTP: &httpctx.Source{URL: "ftp://example.com/ctx.tar", Mode: "runtime"},
	}); err == nil {
		t.Fatal("non-http scheme must be rejected in runtime mode")
	}
}

func TestFailoverOnUnavailableRuntime(t *testing.T) {
	// Simulates a runtime whose container was deleted under the pool:
	// buildkit returns gRPC Unavailable, which must fail over.
	solver := &fakeBuilder{}
	solver.failNext = func() error {
		return fmt.Errorf("builder: solve: failed to get status: %w",
			grpcstatus.Error(grpccodes.Unavailable, "connection refused"))
	}
	f := New(runtime.NewPool(&countingProvider{}), solver)
	res, err := f.Build(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attempts != 2 {
		t.Fatalf("want failover on 2nd attempt, got %d", res.Attempts)
	}
}

func TestPreflightRejectsBeforeClone(t *testing.T) {
	// No capacity anywhere and no git repo at the URL: the preflight must
	// reject with ErrNoCapacity WITHOUT attempting the clone.
	zero := &countingProvider{zeroCap: true}
	f := New(runtime.NewPool(zero), &fakeBuilder{}, WithStore(nil))
	_, err := f.Build(context.Background(), Request{
		Git: &gitctx.Source{URL: "/nonexistent/repo", Mode: "clone"},
	})
	if !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("want ErrNoCapacity, got %v", err)
	}
}

func TestCacheRefFor(t *testing.T) {
	for _, tt := range []struct {
		base, out, git, want string
	}{
		{"reg:5000/buildfleet-cache", "172.16.1.1:3050/team/demo:gitea", "", "reg:5000/buildfleet-cache/team/demo"},
		{"reg:5000/buildfleet-cache", "172.16.1.1:3050/demo:gitea", "", "reg:5000/buildfleet-cache/demo"},
		{"reg:5000/buildfleet-cache", "172.16.1.1:3050/team/app@sha256:xx", "", "reg:5000/buildfleet-cache/team/app"},
		{"reg:5000/buildfleet-cache", "", "http://gitea:3030/bfadmin/demo.git", "reg:5000/buildfleet-cache/bfadmin-demo"},
		{"reg:5000/buildfleet-cache", "", "http://gitea:3030/team/app.git", "reg:5000/buildfleet-cache/team-app"},
		{"reg:5000/buildfleet-cache", "", "", ""}, // no identity: skip caching
		{"", "host/demo:t", "http://x/y.git", ""}, // no cache registry
	} {
		if got := cacheRefFor(tt.base, tt.out, tt.git); got != tt.want {
			t.Errorf("cacheRefFor(%q,%q,%q) = %q, want %q", tt.base, tt.out, tt.git, got, tt.want)
		}
	}
}

func TestBuildSingleflight(t *testing.T) {
	var calls int32
	f := New(runtime.NewPool(&countingProvider{}), &fakeBuilder{slowDigest: "sha256:same", digestCount: &calls})

	req := Request{ContextDigest: "sha256:ctx", Dockerfile: "Dockerfile", OutputRef: "reg/app:1"}
	var wg sync.WaitGroup
	results := make([]*BuildResult, 5)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := f.Build(context.Background(), req)
			if err != nil {
				t.Error(err)
			}
			results[i] = r
		}(i)
	}
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("identical concurrent builds ran %d times, want 1", got)
	}
	for i, r := range results {
		if r == nil || r.Digest != "sha256:same" {
			t.Fatalf("waiter %d got %+v", i, r)
		}
	}

	// A different output ref must not join the flight.
	atomic.StoreInt32(&calls, 0)
	req2 := req
	req2.OutputRef = "reg/app:2"
	if _, err := f.Build(context.Background(), req2); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("different request ran %d times, want 1 (its own)", got)
	}
}

func TestBuildDedupeWindow(t *testing.T) {
	var calls int32
	f := New(runtime.NewPool(&countingProvider{}),
		&fakeBuilder{slowDigest: "sha256:same", digestCount: &calls},
		WithDedupeWindow(150*time.Millisecond))
	req := Request{ContextDigest: "sha256:ctx", Dockerfile: "Dockerfile", OutputRef: "reg/app:1"}

	if _, err := f.Build(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Finished within the window: reused, no new solve.
	if _, err := f.Build(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("in-window repeat rebuilt: %d calls", got)
	}
	// After expiry: rebuilds.
	time.Sleep(200 * time.Millisecond)
	if _, err := f.Build(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expired repeat did not rebuild: %d calls", got)
	}

	// Zero window: concurrency-only dedup.
	f0 := New(runtime.NewPool(&countingProvider{}),
		&fakeBuilder{slowDigest: "sha256:same"}, WithDedupeWindow(0))
	req0 := req
	if _, err := f0.Build(context.Background(), req0); err != nil {
		t.Fatal(err)
	}
}

func TestDedupeWindowZeroRebuilds(t *testing.T) {
	var calls int32
	f := New(runtime.NewPool(&countingProvider{}),
		&fakeBuilder{slowDigest: "sha256:same", digestCount: &calls})
	req := Request{ContextDigest: "sha256:ctx", Dockerfile: "Dockerfile", OutputRef: "reg/app:1"}
	for i := 0; i < 2; i++ {
		if _, err := f.Build(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("zero window must rebuild, got %d calls", got)
	}
}

// gitCommitSolver emits a git-source vertex and its ls-remote log line.
type gitCommitSolver struct{}

func (gitCommitSolver) Solve(ctx context.Context, addr string, in builder.SolveInput, status chan *client.SolveStatus) (*builder.Result, error) {
	if status != nil {
		vd := digest.FromString("loadgit")
		status <- &client.SolveStatus{
			Vertexes: []*client.Vertex{{Digest: vd, Name: "[internal] load git source http://x/demo.git#main"}},
			Logs: []*client.VertexLog{{
				Vertex: vd,
				Data:   []byte("a26b4ac6ba0f3020054e682d18ee03a91854b3ad\trefs/heads/main\n"),
			}},
		}
	}
	close(status)
	return &builder.Result{Digest: "sha256:ok"}, nil
}

func TestRuntimeCommitFromEvents(t *testing.T) {
	f := New(runtime.NewPool(&countingProvider{}), gitCommitSolver{})
	var seen []builder.Event
	res, err := f.Build(context.Background(), Request{
		Git: &gitctx.Source{URL: "http://x/demo.git", Ref: "main", Mode: "runtime"},
	}, WithProgress(func(ev builder.Event) bool {
		seen = append(seen, ev)
		return true
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit != "a26b4ac6ba0f3020054e682d18ee03a91854b3ad" {
		t.Fatalf("commit from events = %q", res.Commit)
	}
	if res.Ref != "main" {
		t.Fatalf("ref = %q", res.Ref)
	}
	if len(seen) == 0 {
		t.Fatal("events must still forward to the caller")
	}
}
