// Package fleet is the public SDK facade: acquire runtime -> solve ->
// release, with node-class failover bounded by maxRetry (DESIGN.md §3).
package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

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

type Request struct {
	// Git builds from a git source; the caller-side clone is pushed to
	// the store and normalized to ContextDigest. Requires WithStore.
	Git *gitctx.Source
	// HTTP builds from an http(s) file URL (tar/tar.gz archive or a
	// single file); same caller-side flow as Git.
	HTTP          *httpctx.Source
	ContextDigest string
	Dockerfile    string
	// DockerfileContent overrides the Dockerfile in the context
	// entirely (out-of-band via the buildkit session).
	DockerfileContent string
	Args              map[string]string
	Platforms         []string
	OutputRef         string

	Resources   resource.Resources
	StartupCost time.Duration
	// Provider pins the build to the named provider (config name);
	// empty = any provider per preference order.
	Provider string
	// PinnedCommit optionally pins a git source to a pre-resolved commit
	// SHA: the server-side clone still fetches by Git.Ref (a SHA ref forces
	// a full clone) and verifies the checked-out HEAD against it, failing
	// deterministically when the ref moved. Zero value = unpinned.
	PinnedCommit string
	// Creds is the per-request credential table (longest-prefix match).
	// Registry entries go to the solve session; git entries feed the
	// git source clone (DESIGN.md §7.1).
	Creds creds.Table

	// contextDir is set by materializeContext; input() prefers it.
	contextDir string
}

func (r Request) spec() runtime.Spec {
	return runtime.Spec{
		Resources:   r.Resources,
		Platforms:   r.Platforms,
		StartupCost: r.StartupCost,
		Provider:    r.Provider,
		Affinity:    r.ProjectKey(),
	}
}

// ProjectKey derives the instance-affinity key: the source URL (host +
// path, branch deliberately excluded — cache mounts are additive and
// benefit from cross-branch sharing); fallback to the pushed image's
// repo path; empty when neither exists.
func (r Request) ProjectKey() string {
	if r.Git != nil && r.Git.URL != "" {
		if u, err := url.Parse(r.Git.URL); err == nil && u.Host != "" {
			p := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
			if p != "" {
				return u.Host + "/" + p
			}
		}
	}
	if r.HTTP != nil && r.HTTP.URL != "" {
		if u, err := url.Parse(r.HTTP.URL); err == nil && u.Host != "" {
			if p := strings.Trim(u.Path, "/"); p != "" {
				return u.Host + "/" + p
			}
		}
	}
	return imageRepoPath(r.OutputRef)
}

func (r Request) input(cfg *buildConfig) builder.SolveInput {
	in := builder.SolveInput{
		GitURL:            cfg.gitURL,
		HTTPURL:           cfg.httpURL,
		GitSecrets:        cfg.gitSecrets,
		HTTPSecrets:       cfg.httpSecrets,
		ContextDigest:     r.ContextDigest,
		ContextDir:        r.contextDir,
		Dockerfile:        r.Dockerfile,
		DockerfileContent: r.DockerfileContent,
		Args:              r.Args,
		Platforms:         r.Platforms,
		OutputRef:         r.OutputRef,
		Creds:             r.Creds,
	}
	if r.contextDir != "" {
		in.ContextDigest = "ctx://" + r.ContextDigest
	}
	return in
}

type BuildResult struct {
	Digest   string
	ImageRef string
	Attempts int
	// Ref/Commit echo the git source when Request.Git was used (ref as
	// requested, commit as actually resolved); empty otherwise.
	Ref    string
	Commit string
	// RuntimeID identifies the runtime instance that produced the image
	// (container id / pod name), for correlating with pool status.
	RuntimeID string
	// Provider is the config name of the provider that ran the build.
	Provider string
}

type solver interface {
	Solve(ctx context.Context, addr string, in builder.SolveInput, status chan *client.SolveStatus) (*builder.Result, error)
}

type builderSolver struct{ b *builder.Builder }

func (s builderSolver) Solve(ctx context.Context, addr string, in builder.SolveInput, status chan *client.SolveStatus) (*builder.Result, error) {
	return s.b.Solve(ctx, addr, in, status)
}

type Fleet struct {
	pool     *runtime.Pool
	solver   solver
	maxRetry int
	store    store.Store // optional: resolves ContextDigest to a local dir

	taskMu sync.Mutex
	tasks  map[string]TaskInfo // runtime ID -> in-flight build
	// providerCache maps provider config name -> cache ref for solves
	// on that provider's runtimes; empty = no cache round-trip. pcMu
	// guards it: runtime provider registration mutates the table while
	// builds read it.
	providerCache map[string]string
	pcMu          sync.Mutex
	flMu          sync.Mutex
	flights       map[string]*flight // in-flight (and freshly finished) builds by request identity
	// dedupeWindow is how long a finished build's result is reused for
	// identical requests; 0 = concurrency-only dedup.
	dedupeWindow time.Duration
}

// TaskInfo describes one in-flight build for Status.
type TaskInfo struct {
	RuntimeID string
	Source    string // git URL or context digest
	OutputRef string
	Started   time.Time
	Attempt   int
}

// BuildOption tunes a single Build call.
type BuildOption func(*buildConfig)

type buildConfig struct {
	progress     func(builder.Event) bool
	progressDone chan struct{} // closed when event forwarding finished
	ref          string        // requested git ref, for BuildResult echo
	commit       string        // resolved git commit
	commitMu     sync.Mutex    // guards commit and refAds (set from the event goroutine)
	refAds       []gitRefAdv   // full ls-remote advertisement from the git-source vertex logs
	gitURL       string        // runtime-mode normalized git context URL
	httpURL      string        // runtime-mode normalized http context URL
	gitSecrets   map[string]builder.SourceSecret
	httpSecrets  map[string]builder.SourceSecret
}

// WithProgress streams live solve events (vertex/log) to fn for this build.
func WithProgress(fn func(builder.Event) bool) BuildOption {
	return func(c *buildConfig) { c.progress = fn }
}

// statusFor wires the solve status channel to cfg.progress. It returns
// the channel to hand the solver and records progressDone in cfg; Build
// waits on it before returning so no progress event is lost to a faster
// solve result. The solver contract requires closing the channel.
func (f *Fleet) statusFor(cfg *buildConfig) chan *client.SolveStatus {
	if cfg.progress == nil {
		return nil
	}
	ch := make(chan *client.SolveStatus, 16)
	done := make(chan struct{})
	cfg.progressDone = done
	go func() {
		defer close(done)
		// Watch returns when the solver closes the channel; if fn stops
		// early we must still drain to avoid blocking the solve.
		_ = builder.Watch(ch, func(ev builder.Event) bool { return cfg.progress(ev) })
		for range ch {
		}
	}()
	return ch
}

type Option func(*Fleet)

// WithMaxRetry bounds total attempts (default 3).
func WithMaxRetry(n int) Option { return func(f *Fleet) { f.maxRetry = n } }

// WithStore enables ContextDigest resolution: each build untars the digest
// into a temp dir (GC-protected for the build's duration) and streams it
// to the runtime via the solve session.
func WithStore(s store.Store) Option { return func(f *Fleet) { f.store = s } }

// WithProviderCache sets the per-provider layer-cache refs (provider
// config name -> registry ref).
func WithProviderCache(m map[string]string) Option {
	return func(f *Fleet) { f.providerCache = m }
}

// SetProviderCache registers (or with an empty base, removes) one
// provider's layer-cache base ref at serving time; runtime provider
// registration calls it while builds consult the table.
func (f *Fleet) SetProviderCache(name, base string) {
	f.pcMu.Lock()
	defer f.pcMu.Unlock()
	if base == "" {
		delete(f.providerCache, name)
		return
	}
	f.providerCache[name] = base
}

// providerCacheBase is the locked read Build resolves the cache ref
// through.
func (f *Fleet) providerCacheBase(name string) string {
	f.pcMu.Lock()
	defer f.pcMu.Unlock()
	return f.providerCache[name]
}

// WithDedupeWindow sets how long identical repeat builds reuse the last
// result instead of rebuilding (0 = concurrency-only dedup).
func WithDedupeWindow(d time.Duration) Option {
	return func(f *Fleet) { f.dedupeWindow = d }
}

func New(pool *runtime.Pool, s solver, opts ...Option) *Fleet {
	f := &Fleet{pool: pool, solver: s, maxRetry: 3, tasks: map[string]TaskInfo{},
		flights: map[string]*flight{}, providerCache: map[string]string{}}
	if s == nil {
		f.solver = builderSolver{b: builder.New()}
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

// Build runs acquire -> solve -> release per attempt. Node-class errors
// fail over (lease.Fail, next runtime); other errors surface immediately.
// Concurrent identical requests share one execution (singleflight): the
// first runs, the rest block and return its result or error.
func (f *Fleet) Build(ctx context.Context, req Request, opts ...BuildOption) (*BuildResult, error) {
	var cfg buildConfig
	for _, o := range opts {
		o(&cfg)
	}
	fl, leader := f.beginFlight(flightKey(req))
	if !leader {
		if cfg.progress != nil {
			cfg.progress(builder.Event{Log: &client.VertexLog{
				Vertex: "internal",
				Data:   []byte("waiting for an identical in-flight build\n"),
			}})
		}
		select {
		case <-fl.done:
			return fl.res, fl.err
		case <-ctx.Done():
			return nil, fmt.Errorf("fleet: build: %w", ctx.Err())
		}
	}
	res, err := f.build(ctx, req, &cfg)
	f.endFlight(fl, res, err)
	return res, err
}

// flight tracks one in-flight build; identical requests join it.
type flight struct {
	key       string
	done      chan struct{}
	res       *BuildResult
	err       error
	expiresAt time.Time
}

// beginFlight returns the flight to join (leader=false) or registers
// the caller as leader. A finished-but-unexpired flight is joined as a
// cache hit; an expired one is replaced.
func (f *Fleet) beginFlight(key string) (*flight, bool) {
	f.flMu.Lock()
	defer f.flMu.Unlock()
	if fl, ok := f.flights[key]; ok {
		select {
		case <-fl.done:
			if f.dedupeWindow <= 0 || time.Now().Before(fl.expiresAt) {
				return fl, false
			}
		default:
			return fl, false // still running
		}
	}
	fl := &flight{key: key, done: make(chan struct{})}
	f.flights[key] = fl
	return fl, true
}

func (f *Fleet) endFlight(fl *flight, res *BuildResult, err error) {
	f.flMu.Lock()
	fl.res, fl.err = res, err
	if f.dedupeWindow > 0 {
		fl.expiresAt = time.Now().Add(f.dedupeWindow)
	} else {
		delete(f.flights, fl.key)
	}
	f.flMu.Unlock()
	close(fl.done)
}

// flightKey identifies requests whose concurrent execution would be
// duplicate work: same source, dockerfile, args, platforms, push target
// and credential material. Provider is excluded (scheduling detail, same
// artifact).
func flightKey(req Request) string {
	parts := []string{req.ContextDigest, req.Dockerfile, req.OutputRef, req.Provider}
	parts = append(parts, sortedKV(req.Args)...)
	parts = append(parts, strings.Join(req.Platforms, ","))
	if r := req.Git; r != nil {
		parts = append(parts, r.URL, r.Ref, r.Subdir, r.Mode, r.ExpectedCommit, req.PinnedCommit)
	}
	if r := req.HTTP; r != nil {
		parts = append(parts, r.URL, r.Mode)
	}
	if k := credentialKey(req); k != "" {
		parts = append(parts, k)
	}
	return strings.Join(parts, "\x00")
}

// credentialKey fingerprints the per-request credential material so builds
// differing only by credentials never share a flight (result or error reuse
// would silently run with the wrong credential). Hashed, because the key
// lives in a long-lived map and must not carry secret material. Only Direct
// secrets are fingerprinted: env-referenced (AuthRef) material is not known
// at key time.
func credentialKey(req Request) string {
	h := sha256.New()
	if r := req.Git; r != nil {
		_, _ = h.Write([]byte(r.Username + "\x00" + r.Password + "\x00"))
	}
	entries := make([]string, 0, len(req.Creds))
	for _, c := range req.Creds {
		user, secret := "", ""
		if c.Direct != nil {
			user, secret = c.Direct.Username, c.Direct.Secret
		}
		entries = append(entries, string(c.Kind)+"\x00"+c.Match+"\x00"+user+"\x00"+secret)
	}
	sort.Strings(entries)
	for _, e := range entries {
		_, _ = h.Write([]byte(e))
	}
	if req.Git == nil && len(entries) == 0 {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sortedKV(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k+"="+m[k])
	}
	sort.Strings(keys)
	return keys
}

func (f *Fleet) build(ctx context.Context, req Request, cfg *buildConfig) (*BuildResult, error) {
	run, err := f.runSolve(ctx, req, cfg, func(in *builder.SolveInput, owner string) {
		// Layer-cache round-trip follows the serving provider (each
		// provider may own its own cache registry) and the project
		// identity derived from the request.
		in.CacheRef = cacheRefFor(f.providerCacheBase(owner), req.OutputRef, sourceURLof(&req))
	})
	if err != nil {
		return nil, err
	}
	return &BuildResult{
		Digest:    run.res.Digest,
		ImageRef:  run.res.ImageRef,
		Attempts:  run.attempts,
		Ref:       cfg.ref,
		Commit:    cfg.commit,
		RuntimeID: run.runtimeID,
		Provider:  run.provider,
	}, nil
}

// solveRun is one completed solve with the runtime that produced it.
type solveRun struct {
	res       *builder.Result
	runtimeID string
	provider  string
	attempts  int
}

// runSolve is the acquire -> solve -> release attempt loop behind Build and
// ProbeFile: node-class errors fail over to the next runtime (bounded by
// maxRetry), other errors surface immediately. tweak receives the fully
// assembled SolveInput — source and context wiring identical to a real
// build, so overrides cannot change how the context is resolved — plus the
// serving provider's config name (for cache-ref derivation).
func (f *Fleet) runSolve(ctx context.Context, req Request, cfg *buildConfig, tweak func(in *builder.SolveInput, owner string)) (*solveRun, error) {
	cleanup, err := f.prepareContext(ctx, &req, cfg)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var lastErr error
	for attempt := 1; attempt <= f.maxRetry; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("fleet: build: %w", err)
		}
		lease, err := f.pool.Acquire(ctx, req.spec())
		if err != nil {
			return nil, fmt.Errorf("fleet: acquire: %w", err)
		}
		owner := f.pool.OwnerName(lease.Runtime().ID)
		f.trackTask(lease.Runtime().ID, TaskInfo{
			RuntimeID: lease.Runtime().ID,
			Source:    taskSource(&req),
			OutputRef: req.OutputRef,
			Started:   time.Now(),
			Attempt:   attempt,
		})
		if cfg.progress != nil {
			cfg.progress(builder.Event{Start: &builder.StartEvent{
				RuntimeID: lease.Runtime().ID,
				Provider:  owner,
				Addr:      lease.Runtime().Addr,
				Attempt:   attempt,
			}})
		}
		if cfg.gitURL != "" && cfg.commit == "" {
			// Runtime git mode: buildkitd resolves the ref itself (its
			// ls-remote output streams through the git-source vertex
			// logs); harvest the commit from there so the reported
			// commit is by construction the one that was built.
			cfg.progress = commitExtractor(cfg)
		}
		in := req.input(cfg)
		if tweak != nil {
			tweak(&in, owner)
		}
		statusCh := f.statusFor(cfg)
		res, err := f.solver.Solve(ctx, lease.Runtime().Addr, in, statusCh)
		f.awaitProgress(cfg)
		if err == nil {
			lease.Release()
			f.untrackTask(lease.Runtime().ID)
			return &solveRun{res: res, runtimeID: lease.Runtime().ID, provider: owner, attempts: attempt}, nil
		}
		if isNodeClass(err) {
			// Node-class errors include transport hiccups (e.g. a
			// dropped port-forward tunnel) on healthy instances: probe
			// before condemning, keep warm instances in the pool.
			if builder.Alive(ctx, lease.Runtime().Addr) {
				lease.Release()
				f.untrackTask(lease.Runtime().ID)
				lastErr = err
				continue
			}
			lease.Fail()
			f.untrackTask(lease.Runtime().ID)
			lastErr = err
			continue
		}
		lease.Release()
		f.untrackTask(lease.Runtime().ID)
		return nil, fmt.Errorf("fleet: solve: %w", err)
	}
	return nil, fmt.Errorf("fleet: exhausted %d attempts: %w", f.maxRetry, lastErr)
}

// prepareContext resolves git/http/context sources into what Solve needs:
// capacity is validated first (nothing else pays materialization costs),
// runtime-mode sources only rewrite the request, server-mode downloads and
// pushed contexts go through the store. The returned cleanup is always
// non-nil.
func (f *Fleet) prepareContext(ctx context.Context, req *Request, cfg *buildConfig) (func(), error) {
	// Reject before paying materialization costs (git clone, http download,
	// context pull): no warm match and no capacity means Acquire would fail
	// anyway.
	if ok, want, avail := f.pool.Availability(req.spec()); !ok {
		return nil, fmt.Errorf("fleet: acquire: %w (requested cpu=%.4g memory=%s, available cpu=%.4g memory=%s; lower resources or add provider capacity)",
			ErrNoCapacity, want.CPU, want.Memory, avail.CPU, avail.Memory)
	}
	switch {
	case req.Git != nil && req.Git.Mode == "runtime":
		// Runtime-side fetch: buildkitd pulls the repo itself via the
		// "context" frontend attr; no store round-trip.
		if err := f.setRuntimeGit(req, cfg); err != nil {
			return nil, err
		}
		cfg.ref = req.Git.Ref
		slog.Info("fleet: git source delegated to runtime",
			"url", req.Git.URL, "ref", cfg.ref)
	case req.Git != nil && req.ContextDigest == "":
		if f.store == nil {
			return nil, fmt.Errorf("fleet: git source requires a store (WithStore)")
		}
		if err := f.materializeGit(ctx, req, cfg); err != nil {
			return nil, err
		}
	case req.HTTP != nil && req.HTTP.Mode == "runtime":
		// Runtime-side fetch: buildkitd downloads the URL itself (tarball
		// semantics); credentials ride the secrets session. No store
		// round-trip.
		if err := f.setRuntimeHTTP(req, cfg); err != nil {
			return nil, err
		}
		slog.Info("fleet: http source delegated to runtime", "url", req.HTTP.URL)
	case req.HTTP != nil && req.ContextDigest == "":
		if f.store == nil {
			return nil, fmt.Errorf("fleet: http source requires a store (WithStore)")
		}
		if err := f.materializeHTTP(ctx, req, cfg); err != nil {
			return nil, err
		}
	}
	if f.store != nil && req.ContextDigest != "" {
		cleanup, err := f.materializeContext(req)
		if err != nil {
			return nil, err
		}
		return cleanup, nil
	}
	return func() {}, nil
}

// awaitProgress bounds the wait for event forwarding: a solver that never
// closes its status channel cannot stall Build forever.
func (f *Fleet) awaitProgress(cfg *buildConfig) {
	if cfg.progressDone == nil {
		return
	}
	select {
	case <-cfg.progressDone:
	case <-time.After(5 * time.Second):
	}
}

func (f *Fleet) trackTask(id string, info TaskInfo) {
	f.taskMu.Lock()
	defer f.taskMu.Unlock()
	f.tasks[id] = info
}

func (f *Fleet) untrackTask(id string) {
	f.taskMu.Lock()
	defer f.taskMu.Unlock()
	delete(f.tasks, id)
}

// Tasks returns the in-flight builds (one per busy runtime).
func (f *Fleet) Tasks() []TaskInfo {
	f.taskMu.Lock()
	defer f.taskMu.Unlock()
	out := make([]TaskInfo, 0, len(f.tasks))
	for _, t := range f.tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// Stats returns the runtime pool snapshot.
func (f *Fleet) Stats() []runtime.Stat { return f.pool.Stats() }

// cacheRefFor derives the per-project cache ref from the provider's
// cache base: the pushed image's repo path identifies the project; a
// source-only build (git or http) falls back to the source URL path;
// without either there is no stable identity, and sharing one chain
// across unrelated builds would thrash it, so caching is skipped.
func cacheRefFor(base, outputRef, srcURL string) string {
	if base == "" {
		return ""
	}
	if p := imageRepoPath(outputRef); p != "" {
		return base + "/" + p
	}
	if p := gitRepoPath(srcURL); p != "" {
		return base + "/" + p
	}
	return ""
}

// imageRepoPath returns "team/app" from "host:5000/team/app:tag" style
// refs; empty for bare names (no path identity) or empty input.
func imageRepoPath(ref string) string {
	if ref == "" {
		return ""
	}
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) < 2 {
		return ""
	}
	path := parts[1]
	// strip digest, then tag (colon after the last slash only)
	if i := strings.Index(path, "@"); i > 0 {
		path = path[:i]
	}
	if i := strings.LastIndex(path, ":"); i > strings.LastIndex(path, "/") {
		path = path[:i]
	}
	if path == "" {
		return ""
	}
	return path
}

// gitRepoPath flattens "http://host/bfadmin/demo.git" to "bfadmin-demo"
// (plain http file paths flatten the same way).
func gitRepoPath(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Path == "" || u.Path == "/" {
		return ""
	}
	p := strings.TrimSuffix(u.Path, ".git")
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	seg := strings.Split(p, "/")
	return strings.Join(seg, "-")
}

func sourceURLof(req *Request) string {
	if req.Git != nil {
		return req.Git.URL
	}
	if req.HTTP != nil {
		return req.HTTP.URL
	}
	return ""
}

func taskSource(req *Request) string {
	if req.Git != nil {
		return req.Git.URL
	}
	if req.HTTP != nil {
		return req.HTTP.URL
	}
	return req.ContextDigest
}

// hostOf extracts host[:port] for credential matching.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Host
}

func isNodeClass(err error) bool {
	var ne NodeError
	if errors.As(err, &ne) {
		return true
	}
	// A dead/unreachable runtime surfaces as gRPC Unavailable (e.g. the
	// container was removed under the pool): fail over to a fresh one.
	if errors.Is(err, builder.ErrUnreachable) {
		return true
	}
	var gs interface{ GRPCStatus() *grpcstatus.Status }
	return errors.As(err, &gs) && gs.GRPCStatus().Code() == grpccodes.Unavailable
}

// setRuntimeGit normalizes the git URL and derives the per-host secrets
// buildkitd will query over the session.
func (f *Fleet) setRuntimeGit(req *Request, cfg *buildConfig) error {
	src := *req.Git
	// The #ref:subdir fragment scopes the context to the subdir, so the
	// Dockerfile path must resolve relative to it (strip the prefix).
	if src.Subdir != "" {
		if p := strings.TrimPrefix(req.Dockerfile, strings.TrimSuffix(src.Subdir, "/")+"/"); p != req.Dockerfile {
			req.Dockerfile = p
		}
	}
	raw := src.URL
	if rewritten, ok := builder.NormalizeScpGitURL(raw); ok {
		raw = rewritten
	}
	gitURL, err := builder.NormalizeGitURL(raw, src.Ref, src.Subdir)
	if err != nil {
		return err
	}
	cfg.gitURL = gitURL
	sec, err := f.gitSecret(req, src)
	if err != nil {
		return err
	}
	if sec != nil {
		host := hostOf(src.URL)
		if cfg.gitSecrets == nil {
			cfg.gitSecrets = map[string]builder.SourceSecret{}
		}
		cfg.gitSecrets[host] = *sec
	}
	return nil
}

var gitRefLine = regexp.MustCompile(`^([0-9a-f]{40})\s+(refs/\S+)`)

// gitRefAdv is one "SHA refs/..." line of the ls-remote advertisement that
// streams through the git-source vertex logs.
type gitRefAdv struct {
	Commit string
	Ref    string
}

// parseGitRefAdv parses one advertisement line (trimmed). Shared by the
// commit extractor and the full-ref harvest so both read the exact same
// ls-remote output shape.
func parseGitRefAdv(line string) (gitRefAdv, bool) {
	m := gitRefLine.FindStringSubmatch(line)
	if m == nil {
		return gitRefAdv{}, false
	}
	return gitRefAdv{Commit: m[1], Ref: m[2]}, true
}

// commitExtractor wraps a progress callback (nil-safe), harvesting the
// ls-remote advertisement from the "[internal] load git source" vertex logs
// — the same output buildkitd itself resolved the ref with. The first SHA
// becomes the resolved commit; every line is kept in refAds so a ref
// listing (ProbeRefs) can replay the full advertisement.
func commitExtractor(cfg *buildConfig) func(builder.Event) bool {
	inner := cfg.progress
	gitVertices := map[string]bool{}
	return func(ev builder.Event) bool {
		switch {
		case ev.Vertex != nil && strings.HasPrefix(ev.Vertex.Name, "[internal] load git source"):
			gitVertices[string(ev.Vertex.Digest)] = true
		case ev.Log != nil && gitVertices[string(ev.Log.Vertex)]:
			for _, line := range strings.Split(string(ev.Log.Data), "\n") {
				adv, ok := parseGitRefAdv(strings.TrimSpace(line))
				if !ok {
					continue
				}
				cfg.commitMu.Lock()
				first := cfg.commit == ""
				if first {
					cfg.commit = adv.Commit
				}
				cfg.refAds = append(cfg.refAds, adv)
				cfg.commitMu.Unlock()
				if first {
					slog.Info("fleet: git source resolved", "commit", adv.Commit, "source", "buildkitd events")
				}
			}
		}
		if inner != nil {
			return inner(ev)
		}
		return true
	}
}

// gitSecret resolves credentials with the established precedence:
// request credential table (git host match), explicit Username/Password,
// then AuthEnv. http sources resolve through sourceSecret with the same
// precedence.
func (f *Fleet) gitSecret(req *Request, src gitctx.Source) (*builder.SourceSecret, error) {
	return f.sourceSecret(req, src.URL, src.AuthEnv, src.Username, src.Password)
}

// setRuntimeHTTP normalizes the http URL and derives the per-host secret
// buildkitd's http source queries over the session (Token entries are
// wrapped as Bearer by buildkitd; Header entries pass through raw).
func (f *Fleet) setRuntimeHTTP(req *Request, cfg *buildConfig) error {
	httpURL, err := builder.NormalizeHTTPURL(req.HTTP.URL)
	if err != nil {
		return err
	}
	cfg.httpURL = httpURL
	sec, err := f.sourceSecret(req, req.HTTP.URL, req.HTTP.AuthEnv, req.HTTP.Username, req.HTTP.Password)
	if err != nil {
		return err
	}
	if sec != nil {
		if cfg.httpSecrets == nil {
			cfg.httpSecrets = map[string]builder.SourceSecret{}
		}
		cfg.httpSecrets[hostOf(req.HTTP.URL)] = *sec
	}
	return nil
}

// sourceSecret resolves remote-source credentials (git and http alike)
// with the established precedence: request credential table (kind-git
// host match), explicit Username/Password, then AuthEnv.
func (f *Fleet) sourceSecret(req *Request, url, authEnv, username, password string) (*builder.SourceSecret, error) {
	if c := req.Creds.Match(creds.Git, hostOf(url)); c != nil {
		sec, err := c.Resolve()
		if err != nil {
			return nil, fmt.Errorf("fleet: resolve source credential: %w", err)
		}
		return &builder.SourceSecret{Header: basicHeader(sec.Username, sec.Secret)}, nil
	}
	switch {
	case username != "" || password != "":
		return &builder.SourceSecret{Header: basicHeader(username, password)}, nil
	case authEnv != "":
		v, ok := os.LookupEnv(authEnv)
		if !ok {
			return nil, fmt.Errorf("fleet: source auth env %s not set", authEnv)
		}
		user, pass, hasCut := strings.Cut(v, ":")
		if !hasCut { // bare token
			return &builder.SourceSecret{Token: v}, nil
		}
		if user == "x-access-token" || !strings.Contains(pass, ":") && user != "" && pass != "" {
			return &builder.SourceSecret{Header: basicHeader(user, pass)}, nil
		}
		return &builder.SourceSecret{Token: pass}, nil
	default:
		return nil, nil
	}
}

func basicHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// materializeGit clones the git source and records its digest so the
// normal digest path takes over.
func (f *Fleet) materializeGit(ctx context.Context, req *Request, cfg *buildConfig) error {
	src := *req.Git
	// The pin rides the clone-by-original-ref path: fetch as planned, then
	// verify the checkout landed on the pre-resolved commit.
	src.ExpectedCommit = req.PinnedCommit
	if src.AuthEnv == "" && src.Username == "" {
		if c := req.Creds.Match(creds.Git, hostOf(src.URL)); c != nil {
			if sec, err := c.Resolve(); err == nil {
				src.Username, src.Password = sec.Username, sec.Secret
			}
		}
	}
	res, err := gitctx.Fetch(ctx, src)
	if err != nil {
		return fmt.Errorf("fleet: git source: %w", err)
	}
	defer os.Remove(res.TarPath)
	tf, err := os.Open(res.TarPath)
	if err != nil {
		return fmt.Errorf("fleet: git tar: %w", err)
	}
	defer tf.Close()
	dig, err := f.store.Push(ctx, tf)
	if err != nil {
		return fmt.Errorf("fleet: push git context: %w", err)
	}
	req.ContextDigest = dig
	cfg.ref = req.Git.Ref
	cfg.commit = res.Commit
	slog.Info("fleet: git source fetched daemon-side",
		"url", req.Git.URL, "ref", cfg.ref, "commit", cfg.commit, "digest", dig)
	return nil
}

// materializeHTTP downloads the http source and records its digest so the
// normal digest path takes over. Kind-"git" credentials match the download
// host — one source-download credential table serves git and http.
func (f *Fleet) materializeHTTP(ctx context.Context, req *Request, cfg *buildConfig) error {
	src := *req.HTTP
	if src.AuthEnv == "" && src.Username == "" {
		if c := req.Creds.Match(creds.Git, hostOf(src.URL)); c != nil {
			if sec, err := c.Resolve(); err == nil {
				src.Username, src.Password = sec.Username, sec.Secret
			}
		}
	}
	res, err := httpctx.Fetch(ctx, src)
	if err != nil {
		return fmt.Errorf("fleet: http source: %w", err)
	}
	defer os.Remove(res.TarPath)
	tf, err := os.Open(res.TarPath)
	if err != nil {
		return fmt.Errorf("fleet: http tar: %w", err)
	}
	defer tf.Close()
	dig, err := f.store.Push(ctx, tf)
	if err != nil {
		return fmt.Errorf("fleet: push http context: %w", err)
	}
	req.ContextDigest = dig
	slog.Info("fleet: http source fetched daemon-side", "url", req.HTTP.URL, "digest", dig)
	return nil
}

// materializeContext untars the digest into a temp dir, holds a GC ref for
// the build, and records the dir for input().
func (f *Fleet) materializeContext(req *Request) (cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "buildfleet-ctx-*")
	if err != nil {
		return nil, fmt.Errorf("fleet: context dir: %w", err)
	}
	release, err := f.store.AcquireRef(req.ContextDigest)
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("fleet: context ref: %w", err)
	}
	if err := f.store.PullToDir(context.Background(), req.ContextDigest, dir); err != nil {
		release()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("fleet: pull context: %w", err)
	}
	req.contextDir = dir
	return func() {
		release()
		os.RemoveAll(dir)
	}, nil
}
