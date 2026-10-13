// Package workflowadapter wires kite capabilities to the workflow module's
// Deps interface. It is the only kite-side package aware of both
// (modules/README.md contract §1: adapter is the single bridge).
package workflowadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/moby/buildkit/client"
	"gorm.io/gorm"
	"k8s.io/klog/v2"

	"github.com/funcx27/kite/modules/application"
	"github.com/funcx27/kite/modules/buildfleet/builder"
	"github.com/funcx27/kite/modules/buildfleet/fleet"
	"github.com/funcx27/kite/modules/buildfleet/gitctx"
	"github.com/funcx27/kite/modules/buildfleet/httpctx"
	"github.com/funcx27/kite/modules/buildfleet/resource"
	"github.com/funcx27/kite/modules/workflow"
	"github.com/funcx27/kite/modules/workflow/exec"
	"github.com/zxh326/kite/pkg/cluster"
	"github.com/zxh326/kite/pkg/credentialsadapter"
	"github.com/zxh326/kite/pkg/model"
	"github.com/zxh326/kite/pkg/rbac"
	"github.com/zxh326/kite/pkg/utils"
)

// historySource marks audit rows written by the workflow module.
const historySource = "workflow"

// encryptionErrorPrefix marks the sentinel string utils.EncryptString returns
// on failure instead of an error (modules/README.md contract §4).
const encryptionErrorPrefix = "encryption_error: "

// notifyTimeout bounds one webhook delivery; a hung receiver must not occupy
// an engine dispatch slot indefinitely.
const notifyTimeout = 10 * time.Second

// Deps implements workflow.Deps on top of kite's RBAC roles, user model and
// ResourceHistory audit log. BuildImage runs through the embedded buildfleet
// fleet (nil capability = buildfleet not configured, build nodes fail
// explicitly) and DeployApp through the application module's deploy facade,
// which reuses the application module's own Deps wiring (audit rows land
// attributed to the application module, operator = run trigger user).
type Deps struct {
	cm *cluster.ClusterManager
	// fleet is the in-process build capability; nil when the host config
	// has no buildfleet section.
	fleet FleetBuilder
	// creds injects credential refs from the credential center.
	creds credentialsadapter.FleetCredentialInjector
	// app is the application module's Deps wiring for the deploy facade.
	app application.Deps
}

// FleetBuilder is the in-process build capability BuildImage consumes:
// one synchronous fleet build streaming builder events (consumer-side
// interface; satisfied by *buildfleetadapter.Deps). ProviderNames exposes
// the configured provider names for the build-settings API.
type FleetBuilder interface {
	Build(ctx context.Context, req fleet.Request, progress func(builder.Event) bool) (*fleet.BuildResult, error)
	ProviderNames() []string
}

var _ workflow.Deps = (*Deps)(nil)

// NewDeps builds the adapter and wires the module's secret codec; it runs at
// route registration, before any module table is read or written. fleet and
// creds may be nil — buildfleet-unconfigured hosts keep starting, and build
// nodes fail with an explicit error instead of silently no-oping. app is the
// application module's Deps wiring backing the DeployApp facade; nil keeps
// the host starting and fails deploy nodes explicitly.
func NewDeps(cm *cluster.ClusterManager, fleet FleetBuilder, creds credentialsadapter.FleetCredentialInjector, app application.Deps) *Deps {
	d := &Deps{cm: cm, fleet: fleet, creds: creds, app: app}
	workflow.SetSecretCodec(d.EncryptSecret, d.DecryptSecret)
	return d
}

// Migrate creates or updates the workflow module's tables on db.
func Migrate(db *gorm.DB) error {
	return workflow.Migrate(db)
}

func (d *Deps) Authorize(c *gin.Context, resource, verb, cluster, namespace string) bool {
	user := c.MustGet("user").(model.User)
	return rbac.CanAccess(user, resource, verb, cluster, namespace)
}

// AuthorizeUser reports whether the named user may perform verb on resource
// in cluster/namespace — the identity-based check engine node dispatch uses,
// since runs execute outside any request (design §8). The user is loaded
// fresh: CanAccess judges by username/OIDC groups against the current role
// config, and an unknown user is denied.
func (d *Deps) AuthorizeUser(userID uint, resource, verb, cluster, namespace string) bool {
	var user model.User
	if err := model.DB.First(&user, userID).Error; err != nil {
		klog.Errorf("Failed to load user %d for workflow authorization: %v", userID, err)
		return false
	}
	// Disabled users keep their role mappings, so mirror RequireAuth's
	// explicit rejection: in-flight runs of a disabled user must not pass
	// node-level authorization.
	if !user.Enabled {
		return false
	}
	return rbac.CanAccess(user, resource, verb, cluster, namespace)
}

func (d *Deps) OperatorID(c *gin.Context) uint {
	user := c.MustGet("user").(model.User)
	return user.ID
}

// IsAdmin reports whether the request's user holds the host admin role —
// the same judgment as auth.RequireAdmin (design §8: assignees 或 admin).
func (d *Deps) IsAdmin(c *gin.Context) bool {
	user := c.MustGet("user").(model.User)
	return rbac.UserHasRole(user, model.DefaultAdminRole.Name)
}

// UserNames resolves user IDs to their display name (Name, falling back to
// Username); unknown or deleted IDs are absent from the map.
func (d *Deps) UserNames(ids []uint) map[uint]string {
	out := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return out
	}
	var users []model.User
	if err := model.DB.Where("id IN ?", ids).Find(&users).Error; err != nil {
		klog.Errorf("Failed to resolve user names: %v", err)
		return out
	}
	for _, u := range users {
		name := u.Name
		if name == "" {
			name = u.Username
		}
		out[u.ID] = name
	}
	return out
}

func (d *Deps) DB() *gorm.DB {
	return model.DB
}

// EncryptSecret seals plaintext with the host's at-rest encryption
// (utils.EncryptString), the same wire format as model.SecretString.
func (d *Deps) EncryptSecret(plaintext string) (string, error) {
	encrypted := utils.EncryptString(plaintext)
	if detail, ok := strings.CutPrefix(encrypted, encryptionErrorPrefix); ok {
		return "", fmt.Errorf("encrypt secret: %s", detail)
	}
	return encrypted, nil
}

// DecryptSecret opens ciphertext produced by EncryptSecret.
func (d *Deps) DecryptSecret(encrypted string) (string, error) {
	plaintext, err := utils.DecryptString(encrypted)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return plaintext, nil
}

// RecordHistory persists one audit row attributed to the workflow module.
func (d *Deps) RecordHistory(entry workflow.HistoryEntry) {
	if err := model.DB.Create(&model.ResourceHistory{
		ClusterName:     entry.Cluster,
		ResourceType:    entry.ResourceType,
		ResourceName:    entry.ResourceName,
		Namespace:       entry.Namespace,
		OperationType:   entry.Operation,
		OperationSource: historySource,
		ResourceYAML:    entry.YAML,
		PreviousYAML:    entry.PreviousYAML,
		Success:         entry.Success,
		ErrorMessage:    entry.Err,
		OperatorID:      entry.OperatorID,
	}).Error; err != nil {
		// Never log the entry payload: run details may carry parameter values.
		klog.Errorf("Failed to record workflow history: %v", err)
	}
}

// BuildImage runs one image build through the embedded buildfleet fleet
// (design §5.1/§9): the request maps onto fleet.Request, the credential refs
// resolve into git source auth and the per-request registry table, and
// builder events bridge to onLog text lines for the node's LogTail.
func (d *Deps) BuildImage(ctx context.Context, req exec.BuildRequest, onLog func(string)) (exec.BuildResult, error) {
	if d.fleet == nil {
		return exec.BuildResult{}, fmt.Errorf("buildfleet is not configured: add a buildfleet section to the kite config to enable build nodes")
	}
	freq, err := d.toFleetRequest(req)
	if err != nil {
		return exec.BuildResult{}, err
	}
	d.applyPodResources(&freq)
	res, err := d.fleet.Build(ctx, freq, progressLines(onLog))
	if err != nil {
		// Source errors embed the raw client URL (gitctx.Clone), which may
		// carry basic-auth userinfo; node errors are persisted in
		// NodeRun.error and served via the runs API, so redact the same way
		// the REST facade redacts audit rows.
		return exec.BuildResult{}, fmt.Errorf("build image: %s", credentialsadapter.RedactCredentials(err.Error()))
	}
	return exec.BuildResult{ImageRef: res.ImageRef, Digest: res.Digest}, nil
}

// ProviderNames lists the buildfleet provider names for the workflow
// module's build-settings API; empty when buildfleet is not configured.
func (d *Deps) ProviderNames() []string {
	if d.fleet == nil {
		return nil
	}
	return d.fleet.ProviderNames()
}

// ProviderFor resolves the namespace's build-cluster default out of the
// module's build_settings table (module helper, host DB handle); lookup
// failures degrade to "not configured" and are logged.
func (d *Deps) ProviderFor(namespace string) (string, bool) {
	provider, ok, err := workflow.BuildProviderFor(model.DB, namespace)
	if err != nil {
		klog.Errorf("Failed to resolve build provider for namespace %q: %v", namespace, err)
		return "", false
	}
	return provider, ok
}

// toFleetRequest maps exec.BuildRequest onto fleet.Request, resolving the
// credential refs when present. Resolution failures carry the ref name so
// node errors point at the misconfigured credential.
func (d *Deps) toFleetRequest(req exec.BuildRequest) (fleet.Request, error) {
	freq := fleet.Request{
		Dockerfile:        req.Dockerfile,
		DockerfileContent: req.DockerfileContent,
		Args:              req.Args,
		Platforms:         req.Platforms,
		OutputRef:         req.OutputRef,
		Provider:          req.Provider,
	}
	if freq.Dockerfile == "" && freq.DockerfileContent == "" {
		freq.Dockerfile = "Dockerfile"
	}
	switch {
	case req.Git != nil && req.Git.URL != "":
		freq.Git = &gitctx.Source{URL: req.Git.URL, Ref: req.Git.Ref, Subdir: req.Git.Subdir}
	case req.HTTP != nil && req.HTTP.URL != "":
		freq.HTTP = &httpctx.Source{URL: req.HTTP.URL}
	default:
		return freq, fmt.Errorf("build request needs a git or http source")
	}
	if req.GitCredentialRef != "" || req.RegistryCredentialRef != "" {
		if err := d.applyCredentials(&freq, req.GitCredentialRef, req.RegistryCredentialRef); err != nil {
			return freq, err
		}
	}
	return freq, nil
}

// applyCredentials injects the resolved payloads via the credential center's
// shared resolver (git auth onto the git source, registry entries into the
// per-request creds table).
func (d *Deps) applyCredentials(freq *fleet.Request, gitRef, registryRef string) error {
	if d.creds == nil {
		ref := gitRef
		if ref == "" {
			ref = registryRef
		}
		return fmt.Errorf("credential %q cannot be resolved: credential center is not wired", ref)
	}
	return d.creds.InjectFleetCredentials(freq, gitRef, registryRef)
}

// applyPodResources fills unset resource fields from the managed provider
// row named by freq.Provider: its pod shape (cores / Gi) applies to this
// build's runtime. Config-file providers have no row — record-not-found is
// the normal case and skips silently; any other DB failure logs and keeps
// the default shape, a spec lookup must never fail the build.
func (d *Deps) applyPodResources(freq *fleet.Request) {
	if freq.Provider == "" {
		return
	}
	row, err := model.GetBuildProviderByName(freq.Provider)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			klog.Errorf("Failed to load build provider %q pod resources: %v", freq.Provider, err)
		}
		return
	}
	if row.PodCPU > 0 && freq.Resources.CPU == 0 {
		freq.Resources.CPU = row.PodCPU
	}
	if row.PodMemoryGi > 0 && freq.Resources.Memory == 0 {
		freq.Resources.Memory = resource.Bytes(row.PodMemoryGi * float64(resource.Giga))
	}
}

// progressLines bridges builder events to onLog text lines: one line per
// acquired runtime, one per finished vertex, log chunks verbatim (design
// §5.1: the engine keeps only a bounded tail, so keep lines short).
func progressLines(onLog func(string)) func(builder.Event) bool {
	// atLineStart tracks whether the previous log chunk ended with a
	// newline, so synthetic status lines never glue onto a partial line.
	atLineStart := true
	return func(ev builder.Event) bool {
		switch {
		case ev.Start != nil:
			if !atLineStart {
				onLog("\n")
			}
			onLog(fmt.Sprintf("runtime %s ready (provider %s, attempt %d)\n",
				ev.Start.RuntimeID, ev.Start.Provider, ev.Start.Attempt))
			atLineStart = true
		case ev.Vertex != nil && ev.Vertex.Completed != nil:
			if !atLineStart {
				onLog("\n")
			}
			onLog(vertexLine(ev.Vertex))
			atLineStart = true
		case ev.Log != nil:
			onLog(string(ev.Log.Data))
			if n := len(ev.Log.Data); n > 0 {
				atLineStart = ev.Log.Data[n-1] == '\n'
			}
		}
		return true
	}
}

// vertexLine renders one finished vertex as "name status" (status mirrored
// from buildkit lifecycle fields, same derivation as the REST facade).
func vertexLine(v *client.Vertex) string {
	status := "complete"
	switch {
	case v.Error != "":
		status = "failed"
	case v.Cached:
		status = "cached"
	}
	return strings.TrimSpace(v.Name) + " " + status + "\n"
}

// DeployApp publishes an image through the application module's exported
// deploy facade (design §5.2): the module's drift gate runs first —
// unresolved cluster drift fails the node with *application.DriftError, whose
// text names the resolution surface followed by each drifted document's
// paths; per-document RBAC and audit run with the run's trigger user inside
// the application module. The facade streams the publish header and
// per-document apply results onto onLog for the node's log.
func (d *Deps) DeployApp(ctx context.Context, req exec.DeployRequest, onLog func(string)) (exec.DeployResult, error) {
	if d.app == nil {
		return exec.DeployResult{}, fmt.Errorf("application module is not wired: deploy nodes cannot run")
	}
	out, err := application.DeployWithImage(ctx, d.app, req.Space, req.App, req.Cluster, req.Image, req.RegistryCredentialRef, req.OperatorID, req.DryRun, onLog)
	if err != nil {
		var df *application.DriftError
		if errors.As(err, &df) {
			// Enumerate the drifted documents after the wrapped gate text so
			// the node failure points at each drift path.
			var b strings.Builder
			for _, doc := range df.Docs {
				fmt.Fprintf(&b, "\n  %s/%s/%s: %s", doc.Kind, doc.Namespace, doc.Name, strings.Join(doc.Paths, ", "))
			}
			return exec.DeployResult{}, fmt.Errorf("deploy app %s/%s on cluster %q: %w%s",
				req.Space, req.App, req.Cluster, err, b.String())
		}
		return exec.DeployResult{}, fmt.Errorf("deploy app %s/%s on cluster %q: %w", req.Space, req.App, req.Cluster, err)
	}
	res := exec.DeployResult{Revision: out.Revision}
	if len(out.Preview) > 0 {
		b, merr := json.Marshal(out.Preview)
		if merr != nil {
			return exec.DeployResult{}, fmt.Errorf("deploy app %s/%s: encode preview: %w", req.Space, req.App, merr)
		}
		res.Preview = b
	}
	return res, nil
}

// Notify delivers one webhook notification as a JSON POST with a timeout
// (design §5.4). Delivery failures return as errors — notify nodes run with
// continue semantics, so they never block the pipeline.
func (d *Deps) Notify(ctx context.Context, req exec.NotifyRequest) error {
	payload := req.Payload
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("notify: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: notifyTimeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notify: receiver answered %s", resp.Status)
	}
	return nil
}
