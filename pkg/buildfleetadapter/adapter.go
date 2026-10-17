// Package buildfleetadapter wires the buildfleet module into the kite host
// over REST/SSE. It is the only kite-side package aware of both.
package buildfleetadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/moby/buildkit/client"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
	"k8s.io/klog/v2"

	"github.com/funcx27/kite/modules/buildfleet/app"
	"github.com/funcx27/kite/modules/buildfleet/builder"
	"github.com/funcx27/kite/modules/buildfleet/config"
	"github.com/funcx27/kite/modules/buildfleet/fleet"
	"github.com/funcx27/kite/modules/buildfleet/gitctx"
	"github.com/funcx27/kite/modules/buildfleet/httpctx"
	bfresource "github.com/funcx27/kite/modules/buildfleet/resource"
	"github.com/zxh326/kite/pkg/cluster"
	"github.com/zxh326/kite/pkg/credentialsadapter"
	"github.com/zxh326/kite/pkg/model"
	"github.com/zxh326/kite/pkg/rbac"
)

const (
	// authzResource is the pseudo-resource all endpoints authorize against.
	authzResource = "builds"
	// credentialsResource is the credential center pseudo-resource: a build
	// referencing a credential ref consumes that credential, which requires
	// get on the center (usage authorization, design §9) beyond the builds
	// capability — judged host-wide like the center's own CRUD.
	credentialsResource = "credentials"
	// historySource marks audit rows written by the buildfleet module.
	historySource = "buildfleet"
	// heartbeatInterval paces the SSE keep-alive comment lines.
	heartbeatInterval = 15 * time.Second
	// eventBufferSize bounds in-flight progress events between the build
	// callback and the SSE writer, the same margin the module's connect
	// API uses (modules/buildfleet/api/server.go).
	eventBufferSize = 16
)

// Deps holds the assembled buildfleet runtime a host serves over REST.
type Deps struct {
	app *app.App
	// cfg is the loaded buildfleet config. It is retained mutable: the
	// runtime-settings API replaces its default profile at serving time.
	cfg *config.Config
	// cm validates managed build providers' target clusters.
	cm *cluster.ClusterManager
	// providerNames are the provider names of the loaded buildfleet config,
	// served to the workflow module's build-settings API.
	providerNames []string
	// creds injects credential refs from the credential center; nil when the
	// center is not wired and credential passthrough is off.
	creds credentialsadapter.FleetCredentialInjector
}

// New resolves the raw buildfleet config subtree (internal.BuildFleetConfig)
// through the module's own config loader — preserving its env-expansion and
// KnownFields validation — and assembles the runtime via app.Setup. cm backs
// the managed build provider API (cluster validation, kubeconfig export).
// creds may be nil: requests without credential refs stay anonymous either way.
func New(ctx context.Context, cm *cluster.ClusterManager, cfgNode *yaml.Node, creds credentialsadapter.FleetCredentialInjector) (*Deps, error) {
	data, err := yaml.Marshal(cfgNode)
	if err != nil {
		return nil, fmt.Errorf("marshal buildfleet config: %w", err)
	}
	cfg, err := config.LoadFromBytes(data)
	if err != nil {
		// yaml errors quote the re-marshalled buildfleet subtree, so their
		// line numbers are relative to the section, not the config file.
		return nil, fmt.Errorf("load buildfleet config (line numbers are relative to the buildfleet section): %w", err)
	}
	applyRuntimeSettingOverrides(cfg)
	a, err := app.Setup(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("setup buildfleet: %w", err)
	}
	names := make([]string, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		names = append(names, p.Name)
	}
	d := &Deps{app: a, cfg: cfg, cm: cm, providerNames: names, creds: creds}
	d.restoreProviders(ctx)
	go d.syncKubeconfigs(ctx)
	return d, nil
}

// applyRuntimeSettingOverrides overlays the persisted runtime settings row
// onto the runtime profiles before app.Setup, so a restart serves the same
// hosts/registry wiring and build images the runtime-settings API applied.
// A read failure logs and keeps the config-file values.
func applyRuntimeSettingOverrides(cfg *config.Config) {
	row, err := model.GetBuildRuntimeSetting()
	if err != nil {
		klog.Warningf("build runtime settings not applied: %v", err)
		return
	}
	if row == nil {
		return
	}
	def := cfg.DefaultRuntime()
	// The row is the source of truth once saved: these fields replace the
	// config-file values wholesale, matching what PUT persists.
	def.Hosts = nil
	for _, h := range row.Hosts {
		def.Hosts = append(def.Hosts, config.HostEntry{IP: h.IP, Names: h.Names})
	}
	def.InsecureRegistries = row.InsecureRegistries
	def.Mirrors = row.Mirrors
	cfg.SetDefaultRuntime(def)
	cfg.SetRuntimeImages(row.RootfulImage, row.RootlessImage)
}

// ProviderNames merges the config-file providers with the managed build
// providers persisted in the database. It backs the workflow module's
// build-settings API; on a database failure the config names are still
// served.
func (d *Deps) ProviderNames() []string {
	names := append([]string(nil), d.providerNames...)
	rows, err := model.ListBuildProviders()
	if err != nil {
		klog.Errorf("failed to list managed build providers: %v", err)
	} else {
		for _, row := range rows {
			names = append(names, row.Name)
		}
	}
	sort.Strings(names)
	return names
}

// Close drains the module's buildkitd instances; the host calls it when its
// application context is cancelled, after in-flight requests have drained.
func (d *Deps) Close(ctx context.Context) error {
	return d.app.Drain(ctx)
}

// Build runs one build on the embedded fleet, streaming builder events to
// progress (return false stops forwarding). It backs the workflow adapter's
// in-process build path, which maps exec.BuildRequest onto fleet.Request
// itself.
func (d *Deps) Build(ctx context.Context, req fleet.Request, progress func(builder.Event) bool) (*fleet.BuildResult, error) {
	return d.app.Fleet.Build(ctx, req, fleet.WithProgress(progress))
}

// RegisterRoutes mounts the module API under /buildfleet. The host must call
// it before applying its RBAC middleware: the URL shapes are non-resource and
// every handler authorizes itself against the builds pseudo-resource.
func (d *Deps) RegisterRoutes(api *gin.RouterGroup) {
	api.POST("/builds", d.handleBuild)
	api.GET("/status", d.handleStatus)
	api.GET("/cachestat", d.handleCacheStat)
	d.RegisterProviderRoutes(api)
}

// authorize writes the unified 403 body and returns false when the user may
// not perform verb on the builds pseudo-resource. The check carries the
// request's cluster name — the same argument the host RBAC middleware passes —
// so cluster-scoped roles can grant builds per cluster, not only via "*".
func authorize(c *gin.Context, verb string) bool {
	user := c.MustGet("user").(model.User)
	cs := c.MustGet("cluster").(*cluster.ClientSet)
	if rbac.CanAccess(user, authzResource, verb, cs.Name, "") {
		return true
	}
	c.AbortWithStatusJSON(http.StatusForbidden,
		gin.H{"error": rbac.NoAccess(user.Key(), verb, authzResource, "", cs.Name)})
	return false
}

// canUseCredentials reports whether the request's user may consume
// credential center entries; writes the 403 body when denied.
func canUseCredentials(c *gin.Context) bool {
	user := c.MustGet("user").(model.User)
	if rbac.CanAccess(user, credentialsResource, "get", "", "") {
		return true
	}
	c.AbortWithStatusJSON(http.StatusForbidden,
		gin.H{"error": rbac.NoAccess(user.Key(), "get", credentialsResource, "", "")})
	return false
}

// buildRequest is the POST /builds body, mapped onto fleet.Request. Git and
// registry credentials are not accepted inline: the credential ref fields
// name credential center entries resolved server-side (design §9).
type buildRequest struct {
	Git           *gitSource  `json:"git"`
	HTTP          *httpSource `json:"http"`
	ContextDigest string      `json:"contextDigest"`
	Dockerfile    string      `json:"dockerfile"`
	// DockerfileContent overrides the context's Dockerfile entirely.
	DockerfileContent string            `json:"dockerfileContent"`
	Args              map[string]string `json:"args"`
	Platforms         []string          `json:"platforms"`
	OutputRef         string            `json:"outputRef"`
	// Provider pins the build to the named provider; empty = any.
	Provider string `json:"provider"`
	// GitCredentialRef names the credential center entry used for the git
	// fetch; empty = anonymous.
	GitCredentialRef string `json:"gitCredentialRef"`
	// RegistryCredentialRef names the credential center entry used for the
	// registry push; empty = anonymous.
	RegistryCredentialRef string `json:"registryCredentialRef"`
}

type gitSource struct {
	URL    string `json:"url"`
	Ref    string `json:"ref"`
	Subdir string `json:"subdir"`
	Mode   string `json:"mode"`
}

type httpSource struct {
	URL  string `json:"url"`
	Mode string `json:"mode"`
}

func (r *buildRequest) toFleetRequest() (fleet.Request, error) {
	req := fleet.Request{
		ContextDigest:     r.ContextDigest,
		Dockerfile:        r.Dockerfile,
		DockerfileContent: r.DockerfileContent,
		Args:              r.Args,
		Platforms:         r.Platforms,
		OutputRef:         r.OutputRef,
		Provider:          r.Provider,
	}
	if req.Dockerfile == "" {
		req.Dockerfile = "Dockerfile"
	}
	switch {
	case r.Git != nil:
		if r.Git.URL == "" {
			return req, fmt.Errorf("git.url is required")
		}
		req.Git = &gitctx.Source{URL: r.Git.URL, Ref: r.Git.Ref, Subdir: r.Git.Subdir, Mode: r.Git.Mode}
	case r.HTTP != nil:
		if r.HTTP.URL == "" {
			return req, fmt.Errorf("http.url is required")
		}
		req.HTTP = &httpctx.Source{URL: r.HTTP.URL, Mode: r.HTTP.Mode}
	case req.ContextDigest != "":
	default:
		return req, fmt.Errorf("one of git, http or contextDigest is required")
	}
	return req, nil
}

// SSE contract for POST /builds (one event-stream per build; JSON payloads):
//
//	runtime: {"runtimeId","provider","addr","attempt"} — build placed on a
//	         runtime, emitted once per attempt
//	vertex:  {"digest","name","status"} — buildkit vertex lifecycle, status
//	         one of pending|running|complete|cached|failed
//	log:     {"vertex","data"} — raw build output chunk for a vertex (UTF-8)
//	result:  {"digest","imageRef","attempts","ref","commit","runtimeId",
//	          "provider"} — terminal success, ends the stream
//	error:   {"error"} — terminal failure, ends the stream
//
// ": ping" comment lines flow every heartbeatInterval as keep-alive. The
// progress callback only feeds a channel; this handler goroutine is the sole
// writer of the response. A client disconnect cancels the build context, so
// fleet.Build returns and releases the runtime lease.
func (d *Deps) handleBuild(c *gin.Context) {
	if !authorize(c, "create") {
		return
	}
	var req buildRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request body: %v", err)})
		return
	}
	if (req.GitCredentialRef != "" || req.RegistryCredentialRef != "") && !canUseCredentials(c) {
		return
	}
	freq, err := req.toFleetRequest()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.GitCredentialRef != "" || req.RegistryCredentialRef != "" {
		if err := d.injectCredentials(&freq, req.GitCredentialRef, req.RegistryCredentialRef); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	d.applyProviderPodResources(&freq)
	user := c.MustGet("user").(model.User)
	cs := c.MustGet("cluster").(*cluster.ClientSet)

	flusher, ok := startSSE(c)
	if !ok {
		return
	}
	// Cancelling on return is what makes a client disconnect stop the
	// build: buildCtx is a child of the request context.
	buildCtx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	evCh, outCh := d.startBuild(buildCtx, freq, user.ID, cs.Name, req.OutputRef)
	pumpBuildStream(c, flusher, evCh, outCh)
}

// buildOutcome is the terminal result of one Fleet.Build call.
type buildOutcome struct {
	res *fleet.BuildResult
	err error
}

// injectCredentials resolves the two refs into the request via the
// credential center's shared resolver; resolution failures are client errors.
func (d *Deps) injectCredentials(freq *fleet.Request, gitRef, registryRef string) error {
	if d.creds == nil {
		ref := gitRef
		if ref == "" {
			ref = registryRef
		}
		return fmt.Errorf("credential %q cannot be resolved: credential center is not wired", ref)
	}
	return d.creds.InjectFleetCredentials(freq, gitRef, registryRef)
}

// applyProviderPodResources fills the request's unset per-instance resource
// fields from the pinned managed provider's row (its configured buildkitd
// pod shape, cores / GiB). A missing row — a config-file provider or an
// unpinned build — skips silently; a database failure only warns, the row
// refines defaults and must never fail the build.
func (d *Deps) applyProviderPodResources(freq *fleet.Request) {
	if freq.Provider == "" || (freq.Resources.CPU > 0 && freq.Resources.Memory > 0) {
		return
	}
	row, err := model.GetBuildProviderByName(freq.Provider)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			klog.Warningf("failed to load build provider %s for pod resources: %v", freq.Provider, err)
		}
		return
	}
	if freq.Resources.CPU == 0 && row.PodCPU > 0 {
		freq.Resources.CPU = row.PodCPU
	}
	if freq.Resources.Memory == 0 && row.PodMemoryGi > 0 {
		freq.Resources.Memory = bfresource.Bytes(row.PodMemoryGi * float64(bfresource.Giga))
	}
}

// startBuild runs Fleet.Build on a background goroutine: progress events
// funnel through evCh (written by the module's callback goroutine), the
// terminal outcome through outCh (buffered, so this call never leaks the
// goroutine even when the SSE side has already gone). Every termination is
// audited exactly once, on the build goroutine.
func (d *Deps) startBuild(ctx context.Context, freq fleet.Request, operatorID uint, clusterName, outputRef string) (<-chan builder.Event, <-chan buildOutcome) {
	evCh := make(chan builder.Event, eventBufferSize)
	outCh := make(chan buildOutcome, 1)
	go func() {
		res, err := d.app.Fleet.Build(ctx, freq, fleet.WithProgress(func(ev builder.Event) bool {
			select {
			case evCh <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}))
		d.recordHistory(operatorID, clusterName, outputRef, res, err)
		outCh <- buildOutcome{res: res, err: err}
	}()
	return evCh, outCh
}

// pumpBuildStream is the only goroutine that writes the response: progress
// events, 15s ping keep-alives, then the terminal event. Any write failure
// (or request-context cancellation) returns; the caller's deferred cancel
// stops the build.
func pumpBuildStream(c *gin.Context, flusher http.Flusher, evCh <-chan builder.Event, outCh <-chan buildOutcome) {
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case ev := <-evCh:
			if !writeBuildEvent(c, flusher, ev) {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-c.Request.Context().Done():
			return
		case o := <-outCh:
			finishBuild(c, flusher, evCh, o)
			return
		}
	}
}

// finishBuild flushes queued progress events so they precede the terminal
// result/error event. fleet.Build gives the progress callback only a bounded
// wind-down (fleet awaitProgress, 5s), so a straggler event past evCh's
// buffer can be dropped here; the terminal outcome itself rides outCh
// (buffered 1) and is never lost. At this point the build has ended, so a
// failed write only means the client is already gone.
func finishBuild(c *gin.Context, flusher http.Flusher, evCh <-chan builder.Event, o buildOutcome) {
drain:
	for {
		select {
		case ev := <-evCh:
			if !writeBuildEvent(c, flusher, ev) {
				return
			}
		default:
			break drain
		}
	}
	if o.err != nil {
		writeSSE(c, flusher, "error", sseErrorEvent{Error: o.err.Error()})
		return
	}
	writeSSE(c, flusher, "result", sseResultEvent{
		Digest:    o.res.Digest,
		ImageRef:  o.res.ImageRef,
		Attempts:  o.res.Attempts,
		Ref:       o.res.Ref,
		Commit:    o.res.Commit,
		RuntimeID: o.res.RuntimeID,
		Provider:  o.res.Provider,
	})
}

type sseRuntimeEvent struct {
	RuntimeID string `json:"runtimeId"`
	Provider  string `json:"provider"`
	Addr      string `json:"addr"`
	Attempt   int    `json:"attempt"`
}

type sseVertexEvent struct {
	Digest string `json:"digest"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type sseLogEvent struct {
	Vertex string `json:"vertex"`
	Data   string `json:"data"`
}

type sseResultEvent struct {
	Digest    string `json:"digest"`
	ImageRef  string `json:"imageRef"`
	Attempts  int    `json:"attempts"`
	Ref       string `json:"ref"`
	Commit    string `json:"commit"`
	RuntimeID string `json:"runtimeId"`
	Provider  string `json:"provider"`
}

type sseErrorEvent struct {
	Error string `json:"error"`
}

func writeBuildEvent(c *gin.Context, flusher http.Flusher, ev builder.Event) bool {
	switch {
	case ev.Start != nil:
		return writeSSE(c, flusher, "runtime", sseRuntimeEvent{
			RuntimeID: ev.Start.RuntimeID,
			Provider:  ev.Start.Provider,
			Addr:      ev.Start.Addr,
			Attempt:   ev.Start.Attempt,
		})
	case ev.Vertex != nil:
		return writeSSE(c, flusher, "vertex", sseVertexEvent{
			Digest: string(ev.Vertex.Digest),
			Name:   ev.Vertex.Name,
			Status: vertexStatus(ev.Vertex),
		})
	case ev.Log != nil:
		return writeSSE(c, flusher, "log", sseLogEvent{
			Vertex: string(ev.Log.Vertex),
			Data:   string(ev.Log.Data),
		})
	default:
		return true
	}
}

// vertexStatus derives a display status from buildkit lifecycle timestamps,
// mirroring the module's connect API (modules/buildfleet/api/server.go).
func vertexStatus(v *client.Vertex) string {
	switch {
	case v.Error != "":
		return "failed"
	case v.Completed != nil && v.Cached:
		return "cached"
	case v.Completed != nil:
		return "complete"
	case v.Started != nil:
		return "running"
	default:
		return "pending"
	}
}

// startSSE sets the streaming response headers, following
// pkg/resources/pod_handler.go writeSSE so proxies do not buffer the stream.
func startSSE(c *gin.Context) (http.Flusher, bool) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming unsupported"})
		return nil, false
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	return flusher, true
}

// writeSSE writes one SSE event and flushes; false means the client is gone.
func writeSSE(c *gin.Context, flusher http.Flusher, event string, payload any) bool {
	b, err := json.Marshal(payload)
	if err != nil {
		klog.Errorf("failed to marshal buildfleet %s event: %v", event, err)
		return false
	}
	if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

// recordHistory persists one audit row when a build terminates. Request
// payloads are never stored: they may carry Dockerfile content or source
// URLs with embedded tokens, and credential refs (credential center
// references, not secret material) add nothing worth persisting. Error
// text goes through credentialsadapter.RedactCredentials, because
// module-side clone errors embed the full client-supplied source URL.
func (d *Deps) recordHistory(operatorID uint, clusterName, outputRef string, res *fleet.BuildResult, err error) {
	name := outputRef
	if name == "" && res != nil {
		name = res.ImageRef
	}
	if name == "" {
		name = "build"
	}
	row := model.ResourceHistory{
		ClusterName:     clusterName,
		ResourceType:    "build",
		ResourceName:    name,
		OperationType:   "create",
		OperationSource: historySource,
		Success:         err == nil,
		ErrorMessage:    credentialsadapter.RedactCredentials(errString(err)),
		OperatorID:      operatorID,
	}
	if e := model.DB.Create(&row).Error; e != nil {
		klog.Errorf("failed to record buildfleet history: %v", e)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type runtimeStatusJSON struct {
	ID          string  `json:"id"`
	State       string  `json:"state"`
	Addr        string  `json:"addr"`
	NodeName    string  `json:"nodeName"`
	Provider    string  `json:"provider"`
	CPU         float64 `json:"cpu"`
	MemoryBytes int64   `json:"memoryBytes"`
	IdleSeconds int64   `json:"idleSeconds"`
}

type taskStatusJSON struct {
	RuntimeID   string `json:"runtimeId"`
	Source      string `json:"source"`
	OutputRef   string `json:"outputRef"`
	StartedUnix int64  `json:"startedUnix"`
	Attempt     int    `json:"attempt"`
}

// handleStatus reports the runtime pool state and in-flight builds.
func (d *Deps) handleStatus(c *gin.Context) {
	if !authorize(c, "get") {
		return
	}
	stats := d.app.Fleet.Stats()
	runtimes := make([]runtimeStatusJSON, 0, len(stats))
	for _, r := range stats {
		runtimes = append(runtimes, runtimeStatusJSON{
			ID:          r.ID,
			State:       string(r.State),
			Addr:        r.Addr,
			NodeName:    r.NodeName,
			Provider:    r.Owner,
			CPU:         r.CPU,
			MemoryBytes: int64(r.Memory),
			IdleSeconds: int64(r.IdleFor.Seconds()),
		})
	}
	tasks := d.app.Fleet.Tasks()
	taskList := make([]taskStatusJSON, 0, len(tasks))
	for _, t := range tasks {
		taskList = append(taskList, taskStatusJSON{
			RuntimeID:   t.RuntimeID,
			Source:      t.Source,
			OutputRef:   t.OutputRef,
			StartedUnix: t.Started.Unix(),
			Attempt:     t.Attempt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"runtimes": runtimes, "tasks": taskList})
}

type cacheProjectStatJSON struct {
	Provider       string `json:"provider"`
	Project        string `json:"project"`
	Tags           int    `json:"tags"`
	Layers         int    `json:"layers"`
	ExclusiveBytes int64  `json:"exclusiveBytes"`
	SharedBytes    int64  `json:"sharedBytes"`
}

type cacheStatResponse struct {
	TotalBytes int64                  `json:"totalBytes"`
	Projects   []cacheProjectStatJSON `json:"projects"`
}

// handleCacheStat reports per-project cache registry usage; 501 when no
// provider enables a cache registry.
func (d *Deps) handleCacheStat(c *gin.Context) {
	if !authorize(c, "get") {
		return
	}
	if d.app.CacheStatFn == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "no cache registry configured"})
		return
	}
	snaps, err := d.app.CacheStatFn(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	providers := make([]string, 0, len(snaps))
	for p := range snaps {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	resp := cacheStatResponse{Projects: []cacheProjectStatJSON{}}
	for _, p := range providers {
		resp.TotalBytes += snaps[p].TotalBytes
		for _, pr := range snaps[p].Projects {
			resp.Projects = append(resp.Projects, cacheProjectStatJSON{
				Provider:       p,
				Project:        pr.Project,
				Tags:           pr.Tags,
				Layers:         pr.Layers,
				ExclusiveBytes: pr.Exclusive,
				SharedBytes:    pr.Shared,
			})
		}
	}
	c.JSON(http.StatusOK, resp)
}
