package api

import (
	"archive/tar"
	"bytes"
	"context"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	buildfleetv1 "github.com/funcx27/kite/modules/buildfleet/api/buildfleetv1"
	buildfleetv1connect "github.com/funcx27/kite/modules/buildfleet/api/buildfleetv1/buildfleetv1connect"
	"github.com/funcx27/kite/modules/buildfleet/builder"
	"github.com/funcx27/kite/modules/buildfleet/fleet"
	"github.com/funcx27/kite/modules/buildfleet/resource"
	"github.com/funcx27/kite/modules/buildfleet/runtime"
	"github.com/funcx27/kite/modules/buildfleet/store"
	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
)

// streamingSolver emits two events (vertex + log) then succeeds.
type streamingSolver struct{ gotDigest string }

func (s *streamingSolver) Solve(ctx context.Context, addr string, in builder.SolveInput, status chan *client.SolveStatus) (*builder.Result, error) {
	s.gotDigest = in.ContextDigest
	now := time.Now()
	status <- &client.SolveStatus{
		Vertexes: []*client.Vertex{{
			Digest:  digest.FromString("step1"),
			Name:    "COPY hello.txt",
			Started: &now,
		}},
	}
	status <- &client.SolveStatus{
		Logs: []*client.VertexLog{{
			Vertex: digest.FromString("step1"),
			Data:   []byte("hello from build step\n"),
		}},
	}
	close(status)
	return &builder.Result{Digest: "sha256:done", ImageRef: in.OutputRef}, nil
}

func TestBuildStreamsEventsOverHTTPJSON(t *testing.T) {
	st := store.NewLocalStore(t.TempDir())
	dig, err := st.Push(context.Background(), tarball("Dockerfile", "FROM scratch"))
	if err != nil {
		t.Fatal(err)
	}
	solver := &streamingSolver{}
	pool := runtime.NewPool(capableProvider{})
	srv := NewServer(fleet.New(pool, solver, fleet.WithStore(st)), st)

	_, handler := buildfleetv1connect.NewBuildServiceHandler(srv)
	httpSrv := httptest.NewServer(handler)
	defer httpSrv.Close()

	c := buildfleetv1connect.NewBuildServiceClient(httpSrv.Client(), httpSrv.URL,
		connect.WithProtoJSON())
	stream, err := c.Build(context.Background(), connect.NewRequest(&buildfleetv1.BuildRequest{
		Context: &buildfleetv1.BuildRequest_ContextDigest{ContextDigest: dig},
	}))
	if err != nil {
		t.Fatal(err)
	}

	var sawVertex, sawLog, sawResult bool
	for stream.Receive() {
		ev := stream.Msg()
		switch e := ev.Event.(type) {
		case *buildfleetv1.BuildEvent_Vertex:
			sawVertex = true
			if e.Vertex.Name != "COPY hello.txt" {
				t.Fatalf("vertex name: got %q", e.Vertex.Name)
			}
		case *buildfleetv1.BuildEvent_Log:
			sawLog = true
			if string(e.Log.Data) != "hello from build step\n" {
				t.Fatalf("log data: got %q", e.Log.Data)
			}
		case *buildfleetv1.BuildEvent_Result:
			sawResult = true
			if e.Result.Digest != "sha256:done" {
				t.Fatalf("result digest: got %q", e.Result.Digest)
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawVertex || !sawLog || !sawResult {
		t.Fatalf("missing events: vertex=%v log=%v result=%v", sawVertex, sawLog, sawResult)
	}
	if want := "ctx://" + dig; solver.gotDigest != want {
		t.Fatalf("solver saw digest %q want %q", solver.gotDigest, want)
	}
}

func TestBuildErrorEvent(t *testing.T) {
	st := store.NewLocalStore(t.TempDir())
	srv := NewServer(fleet.New(runtime.NewPool(capableProvider{}), &failingSolver{}), st)
	_, handler := buildfleetv1connect.NewBuildServiceHandler(srv)
	httpSrv := httptest.NewServer(handler)
	defer httpSrv.Close()

	c := buildfleetv1connect.NewBuildServiceClient(httpSrv.Client(), httpSrv.URL, connect.WithProtoJSON())
	stream, err := c.Build(context.Background(), connect.NewRequest(&buildfleetv1.BuildRequest{
		Context: &buildfleetv1.BuildRequest_ContextDigest{ContextDigest: "sha256:x"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
		if e, ok := stream.Msg().Event.(*buildfleetv1.BuildEvent_Error); ok {
			if e.Error == "" {
				t.Fatal("error event must carry a message")
			}
			return
		}
	}
	t.Fatalf("want terminal error event, got err=%v", stream.Err())
}

type failingSolver struct{}

func (failingSolver) Solve(ctx context.Context, addr string, in builder.SolveInput, status chan *client.SolveStatus) (*builder.Result, error) {
	close(status)
	return nil, context.DeadlineExceeded
}

// tarball builds a one-file in-memory tar (valid input for PullToDir).
func tarball(name, content string) io.Reader {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
	if err := tw.WriteHeader(hdr); err != nil {
		panic(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		panic(err)
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	return &buf
}

type capableProvider struct{}

func (capableProvider) Provision(ctx context.Context, spec runtime.Spec) (*runtime.Runtime, error) {
	return &runtime.Runtime{ID: "p1", Spec: spec, Addr: "127.0.0.1:1", State: runtime.Warm}, nil
}
func (capableProvider) Dispose(ctx context.Context, id string) error { return nil }
func (capableProvider) Capacity() resource.Resources {
	return resource.Resources{CPU: 64, Memory: 128 * resource.Giga}
}

func TestBuildFromGitContext(t *testing.T) {
	origin := initGitRepo(t)
	st := store.NewLocalStore(t.TempDir())
	solver := &streamingSolver{}
	pool := runtime.NewPool(capableProvider{})
	srv := NewServer(fleet.New(pool, solver, fleet.WithStore(st)), st)

	_, handler := buildfleetv1connect.NewBuildServiceHandler(srv)
	httpSrv := httptest.NewServer(handler)
	defer httpSrv.Close()

	c := buildfleetv1connect.NewBuildServiceClient(httpSrv.Client(), httpSrv.URL, connect.WithProtoJSON())
	stream, err := c.Build(context.Background(), connect.NewRequest(&buildfleetv1.BuildRequest{
		Context: &buildfleetv1.BuildRequest_Git{Git: &buildfleetv1.GitContext{
			Url:    origin,
			Ref:    "master",
			Subdir: "app",
		}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
		if e, ok := stream.Msg().Event.(*buildfleetv1.BuildEvent_Result); ok {
			if e.Result.Digest != "sha256:done" {
				t.Fatalf("digest: %q", e.Result.Digest)
			}
			if e.Result.Ref != "master" {
				t.Fatalf("ref echo: %q", e.Result.Ref)
			}
			if len(e.Result.Commit) != 40 {
				t.Fatalf("commit echo: %q", e.Result.Commit)
			}
			if !strings.HasPrefix(solver.gotDigest, "ctx://sha256:") {
				t.Fatalf("git context not normalized to digest: %q", solver.gotDigest)
			}
			return
		}
	}
	t.Fatalf("want result event, err=%v", stream.Err())
}

func TestBuildFromHTTPContext(t *testing.T) {
	var ctxTar bytes.Buffer
	tw := tar.NewWriter(&ctxTar)
	for name, content := range map[string]string{"Dockerfile": "FROM scratch", "hello.txt": "http ctx"} {
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
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(ctxTar.Bytes())
	}))
	defer files.Close()

	st := store.NewLocalStore(t.TempDir())
	solver := &streamingSolver{}
	pool := runtime.NewPool(capableProvider{})
	srv := NewServer(fleet.New(pool, solver, fleet.WithStore(st)), st)

	_, handler := buildfleetv1connect.NewBuildServiceHandler(srv)
	httpSrv := httptest.NewServer(handler)
	defer httpSrv.Close()

	c := buildfleetv1connect.NewBuildServiceClient(httpSrv.Client(), httpSrv.URL, connect.WithProtoJSON())
	stream, err := c.Build(context.Background(), connect.NewRequest(&buildfleetv1.BuildRequest{
		Context: &buildfleetv1.BuildRequest_Http{Http: &buildfleetv1.HTTPContext{
			Url: files.URL + "/ctx.tar",
		}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
		if e, ok := stream.Msg().Event.(*buildfleetv1.BuildEvent_Result); ok {
			if e.Result.Digest != "sha256:done" {
				t.Fatalf("digest: %q", e.Result.Digest)
			}
			if !strings.HasPrefix(solver.gotDigest, "ctx://sha256:") {
				t.Fatalf("http context not normalized to digest: %q", solver.gotDigest)
			}
			return
		}
	}
	t.Fatalf("want result event, err=%v", stream.Err())
}

// initGitRepo creates a local repo with app/hello.txt committed.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "app", "hello.txt")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("git ctx"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("app/hello.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t"},
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}
