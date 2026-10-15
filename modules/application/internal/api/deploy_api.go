package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/application/internal/render"
	"github.com/funcx27/kite/modules/application/internal/runtime"
	"github.com/funcx27/kite/modules/application/internal/store"
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

type deployRequest struct {
	DryRun bool   `json:"dryRun"`
	Image  string `json:"image,omitempty"`
	// Pending config changes handed over by the wizard: {spec} (form) or
	// {manifests} (YAML mode) are persisted as a revision and applied by this
	// one request — save and publish is a single confirm-publish action
	// (design §6). With {dryRun:true} they are only injected in memory for
	// validation. {expectedRev} guards the persist against concurrent saves.
	Spec        json.RawMessage `json:"spec,omitempty"`
	Manifests   json.RawMessage `json:"manifests,omitempty"`
	ExpectedRev *int            `json:"expectedRev,omitempty"`
	// CopyFrom seeds create mode with another cluster's stored manifests
	// (deep copy); it is only valid while the target config does not exist
	// yet. Empty renders the base from the app's draft spec.
	CopyFrom string `json:"copyFrom,omitempty"`
	// manifestsSource labels caller-provided manifests for the revision
	// record; the credential re-render rides the {manifests} funnel but must
	// not be recorded as a user YAML edit. Not part of the wire format.
	manifestsSource string `json:"-"`
	// CredentialRef sets (empty string clears) the config's pull credential;
	// nil leaves the stored value untouched. It persists with the publish and
	// the upserted pull secret rides the same pipeline.
	CredentialRef *string `json:"credentialRef,omitempty"`
	// PreviewToken echoes the hash a dry-run preview returned; the apply
	// refuses to run when the current diff no longer hashes to it.
	PreviewToken string `json:"previewToken,omitempty"`
	// SaveOnly persists the resolved pending set as a revision without
	// applying it to the cluster (the dialog's 仅保存): the config lands in
	// the pending state until a later publish pushes it.
	SaveOnly bool `json:"saveOnly,omitempty"`
}

type deployDocResult struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Action    string `json:"action"` // create | update
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

type deployResponse struct {
	Success     bool              `json:"success"`
	DryRun      bool              `json:"dryRun"`
	SaveOnly    bool              `json:"saveOnly,omitempty"`
	Results     []deployDocResult `json:"results"`
	Anchored    bool              `json:"anchored"`
	AnchorError string            `json:"anchorError,omitempty"`
	Change      []syncops.DocDiff `json:"change,omitempty"` // diff(manifests, live): net change to push
	// Preview carries the redacted old/new document pair per changed document
	// so the UI can render a YAML diff of the net change (design §6).
	Preview []deployDocPreview `json:"preview,omitempty"`
	// PreviewToken is the stable hash of the diff behind a dry-run preview;
	// the confirming deploy request sends it back and the apply rejects the
	// publish when the current diff no longer hashes to it (design §6).
	PreviewToken string `json:"previewToken,omitempty"`
	LatestRev    int    `json:"latestRev,omitempty"`
}

// deployDocPreview is one changed document's before/after view. Old is the
// normalized live object (absent for creates), new the pending manifest.
type deployDocPreview struct {
	Kind   string          `json:"kind"`
	Name   string          `json:"name"`
	Action string          `json:"action"` // create | update
	Old    json.RawMessage `json:"old,omitempty"`
	New    json.RawMessage `json:"new,omitempty"`
}

// DeployConfig pushes the pending manifest set to the cluster (design §6):
// ensure-namespace, then per-document Create/Update with per-resource RBAC,
// ownership-checked creates and a best-effort anchor read-back. {dryRun:true}
// runs the whole pipeline against the API server with dry-run: All and
// touches neither cluster state beyond reads nor the database. Pending
// changes ({spec}/{manifests}) and {image} are persisted as a revision and
// applied by this same request — the save never happens without the publish.
// A missing config enters create mode: the base set is rendered from the app
// draft spec or copied from another cluster's config, the row and its rev1
// are created (dry-run stages them in memory only), then the regular publish
// pipeline runs.
func (h *Handler) DeployConfig(c *gin.Context) {
	app := h.findAppOr404(c)
	if app == nil {
		return
	}
	id := h.requestIdentity(c)
	var req deployRequest
	if !h.bindDeployRequest(c, &req) {
		return
	}
	cluster := c.Param("cfgCluster")
	if !h.validateDeployCreateReq(c, app, cluster, req) {
		return
	}
	if req.SaveOnly {
		h.saveOnlyConfig(c, app, id, cluster, &req)
		return
	}
	ctx := c.Request.Context()
	cli, err := h.deps.ResolveCluster(ctx, cluster)
	if err != nil {
		h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+cluster)
		return
	}
	cfg, created, ok := h.deployTargetConfig(c, app, id, cluster, &req)
	if !ok {
		return
	}
	// Create mode: the row and its rev1 were born in this request, so a
	// client-sent expectedRev refers to some other config (a wizard round-trip
	// carries the copy source's revision) — the optimistic gate cannot apply
	// to the fresh row and must not 409 the pending persist.
	if created {
		req.ExpectedRev = nil
	}

	// Setting a pull credential on an existing config consumes that
	// credential's secret material, so the reference is gated on
	// credentials:get (host-wide) before anything — neither the credential_ref
	// write below nor the publish pipeline may run for a user the credential
	// center's RBAC denies. The write happens before the pending set is
	// resolved so a change can re-render it with the managed imagePullSecrets
	// reference (rerenderForCredential); create mode wrote it onto the new row.
	if !created && !req.DryRun && req.CredentialRef != nil {
		if !h.updateDeployCredential(c, id, app, cfg, &req) {
			return
		}
	}

	manifests, latestRev, ok := h.deployManifests(c, id, app, cfg, req)
	if !ok {
		return
	}
	h.publishPending(c, ctx, id, app, cfg, cli, manifests, req.DryRun, latestRev, req.PreviewToken)
}

// bindDeployRequest parses the deploy body and enforces the spec/manifests
// mutual exclusion; false means the response is already written.
func (h *Handler) bindDeployRequest(c *gin.Context, req *deployRequest) bool {
	if err := bindOptionalJSON(c, req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return false
	}
	if req.Spec != nil && req.Manifests != nil {
		h.fail(c, http.StatusBadRequest, nil, "provide either spec or manifests, not both")
		return false
	}
	return true
}

// saveOnlyConfig is the deploy dialog's 仅保存 action: it persists the same
// pending set a publish would — {spec}/{manifests} plus a bare {image} edit,
// under the same optimistic rev check — but never touches the cluster, so the
// config lands in the pending state (含待发布修改) until a later publish pushes
// it. The pull credential does not ride a save: the managed secret only
// upserts with a publish.
func (h *Handler) saveOnlyConfig(c *gin.Context, app *store.App, id identity, cluster string, req *deployRequest) {
	if req.CopyFrom != "" {
		h.fail(c, http.StatusBadRequest, nil, "copyFrom is only valid when the config does not exist yet")
		return
	}
	cfg, err := h.findConfig(app.ID, cluster)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		h.fail(c, http.StatusNotFound, nil, "config not found for cluster "+cluster)
		return
	}
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if !h.authorize(c, "update", cfg.Cluster, cfg.Namespace) {
		return
	}
	req.CredentialRef = nil
	_, latestRev, ok := h.deployManifests(c, id, app, cfg, *req)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, deployResponse{Success: true, SaveOnly: true, Results: []deployDocResult{}, LatestRev: latestRev})
}

// validateDeployCreateReq rejects a copyFrom deploy against a config that
// already exists: copyFrom only seeds create mode.
func (h *Handler) validateDeployCreateReq(c *gin.Context, app *store.App, cluster string, req deployRequest) bool {
	if req.CopyFrom == "" {
		return true
	}
	_, err := h.findConfig(app.ID, cluster)
	switch {
	case err == nil:
		h.fail(c, http.StatusBadRequest, nil, "copyFrom is only valid when the config does not exist yet")
		return false
	case !errors.Is(err, gorm.ErrRecordNotFound):
		h.fail(c, http.StatusInternalServerError, err, "")
		return false
	}
	return true
}

// credentialRefValue returns the requested credential reference; nil means
// absent and yields the empty ref (no managed pull secret).
func (r *deployRequest) credentialRefValue() string {
	if r.CredentialRef == nil {
		return ""
	}
	return *r.CredentialRef
}

// deployTargetConfig returns the config the deploy applies to. A missing
// config enters create mode: a real deploy authorizes the create verb and
// persists the config row with its first revision up front — a concurrent
// creation hitting the (AppID, Cluster) unique index surfaces as the 409
// refresh-and-retry — while a dry-run stages the row in memory only.
func (h *Handler) deployTargetConfig(c *gin.Context, app *store.App, id identity, cluster string, req *deployRequest) (*store.AppConfig, bool, bool) {
	cfg, err := h.findConfig(app.ID, cluster)
	switch {
	case err == nil:
		return cfg, false, true
	case !errors.Is(err, gorm.ErrRecordNotFound):
		h.fail(c, http.StatusInternalServerError, err, "")
		return nil, false, false
	}
	if req.DryRun {
		cfg, ok := h.stageCreateTarget(c, app, cluster, req)
		return cfg, true, ok
	}
	if !h.authorize(c, "create", cluster, app.Namespace) {
		return nil, false, false
	}
	// Creating the row consumes the referenced credential's secret material
	// (the managed pull secret rides the publish), so gate like the update
	// path before anything is written.
	if credential := req.credentialRefValue(); credential != "" &&
		!id.authorize(credentialsResource, "get", "", "") {
		h.fail(c, http.StatusForbidden, nil,
			fmt.Sprintf("user does not have permission to use credential %q", credential))
		return nil, false, false
	}
	cfg, ok := h.createDeployTarget(c, app, id, cluster, req)
	if !ok {
		return nil, false, false
	}
	return cfg, true, true
}

// createDeployTarget persists the create-mode config: the base set becomes the
// row and its first revision, with the requested pull credential on the row
// and the managed pull secret reference injected into the base manifests.
// False means the response is already written.
func (h *Handler) createDeployTarget(c *gin.Context, app *store.App, id identity, cluster string, req *deployRequest) (*store.AppConfig, bool) {
	manifests, source, ok := h.encodeCreateBase(c, app, req)
	if !ok {
		return nil, false
	}
	cfg, err := h.createConfigRow(id, app, cluster, manifests, source, req.credentialRefValue())
	if err != nil {
		// A concurrent creation hitting the (AppID, Cluster) unique index is
		// the 409 refresh-and-retry; anything else is a 500.
		h.failPendingPersist(c, err)
		return nil, false
	}
	return cfg, true
}

// stageCreateTarget builds the create-mode config in memory for a dry-run: no
// row, no revision, nothing persisted — the base set only exists so the
// pipeline can validate the publish.
func (h *Handler) stageCreateTarget(c *gin.Context, app *store.App, cluster string, req *deployRequest) (*store.AppConfig, bool) {
	manifests, _, ok := h.encodeCreateBase(c, app, req)
	if !ok {
		return nil, false
	}
	return &store.AppConfig{
		AppID:         app.ID,
		Cluster:       cluster,
		Namespace:     app.Namespace,
		Manifests:     store.SecretJSON(manifests),
		CredentialRef: req.credentialRefValue(),
	}, true
}

// encodeCreateBase builds and encodes the create-mode base set: a copyFrom
// deep copy or the Render(App.Spec) draft, with the requested pull credential
// reference injected; source is the revision source for the first snapshot.
func (h *Handler) encodeCreateBase(c *gin.Context, app *store.App, req *deployRequest) ([]byte, string, bool) {
	docs, err := h.baseConfigDocs(app, req.CopyFrom)
	switch {
	case err == nil:
	case errors.Is(err, gorm.ErrRecordNotFound):
		h.fail(c, http.StatusNotFound, nil, "copy source config not found: "+req.CopyFrom)
		return nil, "", false
	case req.CopyFrom == "":
		// Render failure: the draft spec is missing or invalid.
		h.fail(c, http.StatusBadRequest, err, "")
		return nil, "", false
	default:
		h.fail(c, http.StatusInternalServerError, err, "")
		return nil, "", false
	}
	source := store.RevSourceRender
	if req.CopyFrom != "" {
		source = store.RevSourceCopy
	}
	render.InjectPullSecret(docs, pullSecretName(req.credentialRefValue()))
	data, err := store.EncodeManifests(docs)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return nil, "", false
	}
	return data, source, true
}

// baseConfigDocs builds a new config's initial manifest set: a deep copy of
// the copyFrom cluster's stored manifests (secret bytes ride the stored value,
// never the client), or the render of the app's draft spec. A missing copyFrom
// config reports gorm.ErrRecordNotFound.
func (h *Handler) baseConfigDocs(app *store.App, copyFrom string) ([]unstructured.Unstructured, error) {
	if copyFrom == "" {
		return renderAppSpec(app)
	}
	src, err := h.findConfig(app.ID, copyFrom)
	if err != nil {
		return nil, err
	}
	return store.DecodeManifests(src.Manifests)
}

// createConfigRow runs createConfigTx on its own transaction and records the
// create audit — the entry the original config-creation endpoint wrote.
func (h *Handler) createConfigRow(id identity, app *store.App, cluster string, manifests []byte, source, credentialRef string) (*store.AppConfig, error) {
	var cfg store.AppConfig
	err := h.store.DB().Transaction(func(tx *gorm.DB) error {
		var err error
		cfg, err = createConfigTx(tx, app, cluster, manifests, source, credentialRef, id.operatorID)
		return err
	})
	if err != nil {
		return nil, err
	}
	h.recordAudit(id, store.HistoryEntry{
		Cluster: cluster, ResourceType: authzResource, ResourceName: app.Name,
		Namespace: cfg.Namespace, Operation: "create", YAML: redacted(manifests),
	})
	return &cfg, nil
}

// createConfigTx creates the per-cluster config row with its first revision
// inside the caller's transaction (the shared tail of every config-creation
// funnel); credentialRef is written onto the row.
func createConfigTx(tx *gorm.DB, app *store.App, cluster string, manifests []byte, source, credentialRef string, operatorID uint) (store.AppConfig, error) {
	cfg := store.AppConfig{
		AppID:         app.ID,
		Cluster:       cluster,
		Namespace:     app.Namespace,
		Manifests:     store.SecretJSON(manifests),
		CredentialRef: credentialRef,
	}
	if err := tx.Create(&cfg).Error; err != nil {
		return cfg, err
	}
	_, err := store.AppendRevisionTx(tx, cfg.ID, source, manifests, operatorID, 0)
	return cfg, err
}

// updateDeployCredential applies a CredentialRef change to a persisted config
// before the pending set is resolved; false means the response is already
// written.
func (h *Handler) updateDeployCredential(c *gin.Context, id identity, app *store.App, cfg *store.AppConfig, req *deployRequest) bool {
	if *req.CredentialRef != "" && !id.authorize(credentialsResource, "get", "", "") {
		h.fail(c, http.StatusForbidden, nil,
			fmt.Sprintf("user does not have permission to use credential %q", *req.CredentialRef))
		return false
	}
	if err := h.store.DB().Model(&store.AppConfig{}).Where("id = ?", cfg.ID).
		Update("credential_ref", *req.CredentialRef).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return false
	}
	if *req.CredentialRef != cfg.CredentialRef {
		if err := h.rerenderForCredential(app, cfg, *req.CredentialRef, req); err != nil {
			h.fail(c, http.StatusInternalServerError, err, "")
			return false
		}
	}
	cfg.CredentialRef = *req.CredentialRef
	return true
}

// deployManifests resolves the manifest set the deploy applies — the stored
// set plus any pending {spec}/{manifests}/{image} edit — writing the 4xx
// responses itself; false means the caller must stop.
func (h *Handler) deployManifests(c *gin.Context, id identity, app *store.App, cfg *store.AppConfig, req deployRequest) ([]unstructured.Unstructured, int, bool) {
	stored, err := store.DecodeManifests(cfg.Manifests)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return nil, 0, false
	}
	if len(stored) == 0 {
		h.fail(c, http.StatusBadRequest, nil, "config has no manifests to deploy")
		return nil, 0, false
	}
	manifests, latestRev, ok := h.resolveDeployManifests(c, id, app, cfg, stored, req)
	if !ok {
		return nil, 0, false
	}
	// A dialog image edit on top of a pending set rides the same request and
	// the same revision; the non-dry-run path applied it inside the persist.
	if req.Image != "" && (req.Spec != nil || req.Manifests != nil) && req.DryRun {
		if err := setImageOnPending(manifests, app.Name, req.Image); err != nil {
			h.fail(c, http.StatusBadRequest, err, "")
			return nil, 0, false
		}
	}
	return manifests, latestRev, true
}

// publishPending runs the apply pipeline for the resolved manifest set and
// writes the deploy response.
func (h *Handler) publishPending(c *gin.Context, ctx context.Context, id identity, app *store.App, cfg *store.AppConfig, cli client.Client, manifests []unstructured.Unstructured, dryRun bool, latestRev int, previewToken string) {
	resp, err := h.applyPublish(ctx, id, app, cfg, cli, manifests, dryRun, latestRev, previewToken)
	if !h.publishFailure(c, err) {
		return
	}
	resp.LatestRev = latestRev
	c.JSON(http.StatusOK, resp)
}

// failStalePreview answers a stale previewToken with the shared 409 mapping
// (deploy dry-run pins the token; resolve re-checks it before adopting).
func (h *Handler) failStalePreview(c *gin.Context) {
	h.fail(c, http.StatusConflict, nil, (&previewStaleError{}).Error())
}

// publishFailure maps an applyPublish failure to its HTTP error response;
// false means the caller must stop.
func (h *Handler) publishFailure(c *gin.Context, err error) bool {
	var nf *namespaceForbiddenError
	var stale *previewStaleError
	switch {
	case errors.As(err, &nf):
		h.fail(c, http.StatusForbidden, err, err.Error())
	case errors.As(err, &stale):
		h.failStalePreview(c)
	case err != nil:
		h.fail(c, http.StatusInternalServerError, err, "")
	default:
		return true
	}
	return false
}

// failPendingPersist translates a persistPendingConfig failure; the bool
// result says the caller may continue.
func (h *Handler) failPendingPersist(c *gin.Context, err error) bool {
	if err == nil {
		return true
	}
	switch {
	case errors.Is(err, ErrConfigModified), isConcurrentWriteError(err):
		h.fail(c, http.StatusConflict, nil, ErrConfigModified.Error())
	case errors.Is(err, ErrWorkloadTypeChanged):
		h.fail(c, http.StatusBadRequest, nil, ErrWorkloadTypeChanged.Error())
	default:
		h.fail(c, http.StatusInternalServerError, err, "")
	}
	return false
}

// resolveDeployManifests builds the manifest set this deploy request applies:
// pending {spec}/{manifests} edits persist and ride the publish request's
// revision, a bare {image} edit persists as its own deploy-image revision,
// and dry-run only injects the change in memory (design §6: the save never
// happens without the publish). False means the response is already written.
func (h *Handler) resolveDeployManifests(c *gin.Context, id identity, app *store.App, cfg *store.AppConfig, stored []unstructured.Unstructured, req deployRequest) ([]unstructured.Unstructured, int, bool) {
	var (
		manifests []unstructured.Unstructured
		latestRev int
	)
	switch {
	case req.Spec != nil:
		var spec render.AppSpec
		if err := json.Unmarshal(req.Spec, &spec); err != nil {
			h.fail(c, http.StatusBadRequest, nil, "spec must be a valid AppSpec JSON object: "+err.Error())
			return nil, 0, false
		}
		spec.Normalize(app.Name)
		if err := spec.Validate(); err != nil {
			h.fail(c, http.StatusBadRequest, nil, "invalid spec: "+err.Error())
			return nil, 0, false
		}
		build := func(cur *store.AppConfig, curDocs []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
			// The workload kind is immutable while the config is anchored to
			// cluster resources (design §7): switching Deployment↔StatefulSet
			// would leave the old controller running as unmanaged drift.
			if anchor, err := store.DecodeManifests(cur.LastApplied); err != nil {
				return nil, err
			} else if len(anchor) > 0 {
				if wl := runtime.WorkloadOf(app.Name, curDocs); wl != nil &&
					workloadKindOf(spec.Workload.Type) != wl.GetKind() {
					return nil, ErrWorkloadTypeChanged
				}
			}
			docs, err := render.InjectFields(app.Name, cur.Namespace, curDocs, spec)
			if err != nil {
				return nil, err
			}
			// A config with a pull credential publishes pods that reference
			// the managed pull secret; keep the pending set in lockstep with
			// the credential so the reference never becomes drift.
			render.InjectPullSecret(docs, pullSecretName(cur.CredentialRef))
			return docs, nil
		}
		if req.DryRun {
			injected, err := render.InjectFields(app.Name, cfg.Namespace, stored, spec)
			if err != nil {
				h.fail(c, http.StatusBadRequest, err, "")
				return nil, 0, false
			}
			render.InjectPullSecret(injected, pullSecretName(cfg.CredentialRef))
			restoreMaskedSecretValues(injected, stored)
			manifests = injected
		} else {
			var err error
			manifests, latestRev, err = h.persistPendingConfig(id, app, cfg, req.ExpectedRev, store.RevSourceWizard, build, req.Image)
			if !h.failPendingPersist(c, err) {
				return nil, 0, false
			}
		}
	case req.Manifests != nil:
		incoming, err := store.DecodeManifests(req.Manifests)
		if err != nil {
			h.fail(c, http.StatusBadRequest, err, "")
			return nil, 0, false
		}
		build := func(cur *store.AppConfig, curDocs []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
			return incoming, nil
		}
		source := store.RevSourceYAML
		if req.manifestsSource != "" {
			source = req.manifestsSource
		}
		if req.DryRun {
			manifests = incoming
			restoreMaskedSecretValues(manifests, stored)
		} else {
			var err error
			manifests, latestRev, err = h.persistPendingConfig(id, app, cfg, req.ExpectedRev, source, build, req.Image)
			if !h.failPendingPersist(c, err) {
				return nil, 0, false
			}
		}
	default:
		switch {
		case req.Image != "" && req.DryRun:
			// Dry-run must validate the pending image without persisting it
			// (design §6: dry-run touches neither cluster state nor the DB).
			injected, err := previewImage(stored, app.Name, req.Image)
			if err != nil {
				h.fail(c, http.StatusBadRequest, err, "")
				return nil, 0, false
			}
			manifests = injected
		case req.Image != "":
			var err error
			manifests, latestRev, err = h.persistImage(id, app, cfg, req.Image)
			if err != nil {
				h.fail(c, http.StatusBadRequest, err, "")
				return nil, 0, false
			}
		default:
			manifests = stored
		}
	}
	return manifests, latestRev, true
}

// rerenderForCredential keeps the config's stored manifests referencing the
// managed pull secret across credential changes: the reference is injected
// into (or removed from) the CURRENT manifests — never a fresh render, which
// would clobber runtime changes such as the image a workflow deploy pushed —
// and rides the {manifests} funnel as a revision, so stored manifests, the
// applied set and the live cluster stay identical and the reference never
// becomes drift.
func (h *Handler) rerenderForCredential(app *store.App, cfg *store.AppConfig, credentialRef string, req *deployRequest) error {
	docs, err := store.DecodeManifests(cfg.Manifests)
	if err != nil {
		return fmt.Errorf("decode stored manifests: %w", err)
	}
	render.InjectPullSecret(docs, pullSecretName(credentialRef))
	data, err := store.EncodeManifests(docs)
	if err != nil {
		return fmt.Errorf("encode manifests: %w", err)
	}
	req.Manifests = json.RawMessage(data)
	req.manifestsSource = store.RevSourcePullCred
	return nil
}

// applyPublish is the apply core of the publish pipeline (design §6):
// ensure-namespace, live fetch, per-document Create/Update and the anchor
// read-back; it ends with the net-change diff group. A non-empty previewToken
// is checked against the hash of the current diff before any write (design
// §6: the confirmed preview must describe the cluster state being pushed).
// With dryRun the apply only validates against the API server and the
// response carries the previewToken of that diff. appliedRev is the revision
// the applied set was persisted as (0 — a bare republish of the stored set —
// resolves to the newest revision when the result is written).
func (h *Handler) applyPublish(ctx context.Context, id identity, app *store.App, cfg *store.AppConfig, cli client.Client, manifests []unstructured.Unstructured, dryRun bool, appliedRev int, previewToken string) (deployResponse, error) {
	if !dryRun {
		if err := h.ensureNamespace(ctx, id, cli, cfg.Cluster, cfg.Namespace); err != nil {
			return deployResponse{}, err
		}
	}

	anchor, err := store.DecodeManifests(cfg.LastApplied)
	if err != nil {
		return deployResponse{}, err
	}
	templates := syncops.IdentityTemplates(manifests, anchor)
	live, err := syncops.FetchLiveDocs(ctx, cli, templates)
	if err != nil {
		return deployResponse{}, err
	}

	// The diff group runs on normalized live objects: raw ones carry
	// server-managed fields (status, clusterIP, last-applied annotations)
	// that would surface as fake cluster-side drift. The apply path below
	// keeps the raw objects — Update needs the resourceVersion.
	liveForDiff := make(map[string]*unstructured.Unstructured, len(live))
	for key, doc := range live {
		liveForDiff[key] = syncops.Normalize(doc)
	}

	change := syncops.DiffManifests(manifests, syncops.LiveList(liveForDiff, manifests))
	if !dryRun && previewToken != "" && previewTokenHash(change) != previewToken {
		return deployResponse{}, &previewStaleError{}
	}

	// The managed image pull secret rides every publish of a config with a
	// credential: manual (DeployConfig), facade (DeployWithImage) and
	// rollback all funnel here, so the namespace never publishes without it.
	if !dryRun && cfg.CredentialRef != "" {
		if err := h.ensurePullSecret(ctx, id, cli, cfg); err != nil {
			return deployResponse{}, err
		}
	}
	// Managed TLS secrets ride the same funnels: every certificate the
	// pending Ingresses reference is upserted before the Ingress itself
	// applies, so the referenced secret never lags the rules.
	if !dryRun {
		if err := h.ensureTLSSecret(ctx, id, cli, cfg, manifests); err != nil {
			return deployResponse{}, err
		}
	}

	anchorByKey := docMap(anchor)
	results := make([]deployDocResult, 0, len(manifests))
	anchorReadBacks := make([]unstructured.Unstructured, 0, len(manifests))
	failedKeys := map[string]bool{}
	anchorErr := ""
	for i := range manifests {
		doc, err := namespacedCopy(&manifests[i], cfg.Namespace)
		key := store.DocKeyOf(doc)
		result := deployDocResult{Kind: doc.GetKind(), Namespace: doc.GetNamespace(), Name: doc.GetName(), Success: true}
		if err != nil {
			result.Success, result.Error = false, err.Error()
			results = append(results, result)
			failedKeys[key] = true
			continue
		}
		action, applyErr := h.applyDoc(ctx, id, cli, app.Name, cfg, doc, live[key], anchorByKey[key], dryRun)
		result.Action = action
		if applyErr != nil {
			result.Success, result.Error = false, applyErr.Error()
			failedKeys[key] = true
		} else if !dryRun {
			rb, rbErr := readBack(ctx, cli, doc)
			if rbErr != nil {
				anchorErr = rbErr.Error()
			} else {
				anchorReadBacks = append(anchorReadBacks, *rb)
			}
		}
		results = append(results, result)
	}

	allOK := len(failedKeys) == 0
	if !dryRun {
		if err := h.persistDeployResult(cfg, anchor, anchorReadBacks, failedKeys, allOK, appliedRev); err != nil {
			return deployResponse{}, err
		}
	}

	resp := deployResponse{
		Success:     allOK,
		DryRun:      dryRun,
		Results:     results,
		Anchored:    anchorErr == "" && !dryRun,
		AnchorError: anchorErr,
		Change:      change,
		Preview:     previewDocs(manifests, liveForDiff, change),
	}
	if dryRun {
		resp.PreviewToken = previewTokenHash(change)
	}
	return resp, nil
}

// previewTokenHash derives the stable preview token of a diff result: the
// hex-encoded SHA-256 prefix of its canonical JSON. The input is server-built
// and marshalling cannot fail; a failure yields the empty token, which never
// matches a client-sent one.
func previewTokenHash(diff []syncops.DocDiff) string {
	data, err := json.Marshal(diff)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// previewStaleError marks a previewToken that no longer matches the current
// cluster diff, so callers answer 409 with a preview-again hint.
type previewStaleError struct{}

func (e *previewStaleError) Error() string { return "cluster state changed, preview again" }

// previewDocs pairs each changed document with its normalized live (old) and
// pending (new) view, both redacted. Documents the pending set no longer
// declares are skipped: the apply path never deletes them, so they are not
// part of the pushed change.
func previewDocs(manifests []unstructured.Unstructured, live map[string]*unstructured.Unstructured, change []syncops.DocDiff) []deployDocPreview {
	byKey := make(map[string]*unstructured.Unstructured, len(manifests))
	for i := range manifests {
		byKey[store.DocKeyOf(&manifests[i])] = &manifests[i]
	}
	out := make([]deployDocPreview, 0, len(change))
	for i := range change {
		key := store.DocKey(change[i].Kind, change[i].Namespace, change[i].Name)
		p := deployDocPreview{Kind: change[i].Kind, Name: change[i].Name, Action: "update"}
		if old := live[key]; old != nil {
			oldDoc := old.DeepCopy()
			// Same defaulted-pull-policy absorption the change diff ran on,
			// so the YAML view matches the paths the diff reports.
			if doc := byKey[key]; doc != nil {
				syncops.AbsorbDefaultedPullPolicy(doc, oldDoc)
			}
			p.Old = redactedDocRaw(oldDoc)
		} else {
			p.Action = "create"
		}
		if doc := byKey[key]; doc != nil {
			p.New = redactedDocRaw(doc)
		}
		if p.Old == nil && p.New == nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// persistPendingConfig builds the pending manifest set from the locked config
// row and writes it as the desired state with one revision, under the
// optimistic rev check when expectedRev is set. The masked "***" secret
// markers are restored from the current values.
func (h *Handler) persistPendingConfig(
	id identity,
	app *store.App,
	cfg *store.AppConfig,
	expectedRev *int,
	source string,
	build func(cur *store.AppConfig, curDocs []unstructured.Unstructured) ([]unstructured.Unstructured, error),
	image string,
) ([]unstructured.Unstructured, int, error) {
	var (
		docs   []unstructured.Unstructured
		newRev int
	)
	txErr := h.store.DB().Transaction(func(tx *gorm.DB) error {
		cur, err := lockConfig(tx, cfg.ID)
		if err != nil {
			return err
		}
		if expectedRev != nil {
			var maxRev int
			if err := tx.Model(&store.ConfigRevision{}).
				Where("app_config_id = ?", cur.ID).
				Select("COALESCE(MAX(rev), 0)").
				Scan(&maxRev).Error; err != nil {
				return err
			}
			if *expectedRev != maxRev {
				return ErrConfigModified
			}
		}
		curDocs, err := store.DecodeManifests(cur.Manifests)
		if err != nil {
			return err
		}
		docs, err = build(cur, curDocs)
		if err != nil {
			return err
		}
		if image != "" {
			if err := setImageOnPending(docs, app.Name, image); err != nil {
				return err
			}
		}
		restoreMaskedSecretValues(docs, curDocs)
		data, err := store.EncodeManifests(docs)
		if err != nil {
			return err
		}
		if err := tx.Model(&store.AppConfig{}).Where("id = ?", cur.ID).
			Update("manifests", store.SecretJSON(data)).Error; err != nil {
			return err
		}
		cfg.Manifests = store.SecretJSON(data)
		newRev, err = store.AppendRevisionTx(tx, cur.ID, source, data, id.operatorID, 0)
		return err
	})
	if txErr != nil {
		return nil, 0, txErr
	}
	h.recordAudit(id, store.HistoryEntry{
		Cluster: cfg.Cluster, ResourceType: authzResource, ResourceName: app.Name,
		Namespace: cfg.Namespace, Operation: "update",
		YAML: redacted(cfg.Manifests),
	})
	return docs, newRev, nil
}

// setImageOnPending sets the dialog image edit onto a pending manifest set,
// mutating the workload document in place.
func setImageOnPending(docs []unstructured.Unstructured, appName, image string) error {
	if strings.TrimSpace(image) == "" {
		return nil
	}
	wi := render.FindWorkload(appName, docs)
	if wi < 0 {
		return errors.New("config has no workload document to set the image on")
	}
	if !setImageOnWorkload(&docs[wi], image) {
		return errors.New("config workload has no container to set the image on")
	}
	return nil
}

// namespaceForbiddenError marks a failed ensure-namespace permission check so
// callers answer 403 instead of 500.
type namespaceForbiddenError struct{ msg string }

func (e *namespaceForbiddenError) Error() string { return e.msg }

// ensureNamespace guarantees the config namespace exists on the cluster,
// creating it with the space marker label when missing (design §5/§6).
func (h *Handler) ensureNamespace(ctx context.Context, id identity, cli client.Client, cluster, namespace string) error {
	existing := syncops.BlankDoc(&unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Namespace",
	}})
	existing.SetName(namespace)
	err := cli.Get(ctx, client.ObjectKey{Name: namespace}, existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get namespace %s: %w", namespace, err)
	}
	if !id.authorize("namespaces", "create", cluster, "") {
		return &namespaceForbiddenError{msg: fmt.Sprintf(
			"user does not have permission to create namespace %s on cluster %s", namespace, cluster)}
	}
	ns := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]interface{}{
			"name":   namespace,
			"labels": map[string]interface{}{store.LabelKiteSpace: "true"},
		},
	}}
	if err := cli.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", namespace, err)
	}
	h.recordAudit(id, store.HistoryEntry{
		Cluster: cluster, ResourceType: "namespaces", ResourceName: namespace, Operation: "create",
	})
	return nil
}

// pullSecretName derives the managed image pull secret's name from the
// credential; an empty credential means no managed secret and returns "".
// Credential names are DNS-1123 labels of up to 63 chars, so
// "kite-pull-"+name only fits the 63-char label limit for names up to 53
// chars; longer refs are truncated to a fragment and suffixed with a short
// SHA-256 of the full ref — deterministic across publishes (the update path
// looks the secret up by name) and unique among refs sharing a prefix.
func pullSecretName(credentialRef string) string {
	const (
		prefix    = "kite-pull-"
		maxLabel  = 63
		hashChars = 8 // hex-encoded SHA-256 fragment
	)
	if credentialRef == "" {
		return ""
	}
	if len(prefix)+len(credentialRef) <= maxLabel {
		return prefix + credentialRef
	}
	fragment := strings.TrimRight(credentialRef[:maxLabel-len(prefix)-hashChars-1], "-")
	sum := sha256.Sum256([]byte(credentialRef))
	return prefix + fragment + "-" + hex.EncodeToString(sum[:hashChars/2])
}

const (
	pullSecretType    = "kubernetes.io/dockerconfigjson"
	pullSecretDataKey = ".dockerconfigjson"
	tlsSecretType     = "kubernetes.io/tls"
	// secretsResource is the RBAC resource of both managed secret kinds
	// (image pull secret and TLS secret).
	secretsResource = "secrets"
)

// ensurePullSecret upserts the config namespace's managed image pull secret
// from the config credential's registry entries: .dockerconfigjson is built
// as {"auths":{host:{"auth":base64(user:pass)}}}; git entries carry no pull
// material and are ignored. The update path rewrites only type/data and the
// managed annotation, keeping foreign fields of the live secret. Workloads
// must reference the secret via their own imagePullSecrets — kite never
// rewrites user manifests (form-rendered ones included).
func (h *Handler) ensurePullSecret(ctx context.Context, id identity, cli client.Client, cfg *store.AppConfig) error {
	// Resolving the config credential decrypts its registry entries — the
	// publish consumes the credential, so the acting identity needs
	// credentials:get (host-wide, design §9) before any material is touched.
	// This is the shared gate for every publish funnel (manual deploy,
	// facade, rollback); the facade judges the run's trigger user.
	if !id.authorize(credentialsResource, "get", "", "") {
		return fmt.Errorf("user does not have permission to use credential %q", cfg.CredentialRef)
	}
	auths, err := h.deps.PullSecretData(cfg.CredentialRef)
	if err != nil {
		return fmt.Errorf("resolve pull credential %q: %w", cfg.CredentialRef, err)
	}
	if len(auths) == 0 {
		return fmt.Errorf("credential %q has no registry entries to build an image pull secret", cfg.CredentialRef)
	}
	authsJSON := map[string]interface{}{}
	for host, auth := range auths {
		authsJSON[host] = map[string]interface{}{
			"auth": base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Password)),
		}
	}
	payload, err := json.Marshal(map[string]interface{}{"auths": authsJSON})
	if err != nil {
		return fmt.Errorf("encode %s of pull secret: %w", pullSecretDataKey, err)
	}
	data := map[string]interface{}{
		pullSecretDataKey: base64.StdEncoding.EncodeToString(payload),
	}

	name := pullSecretName(cfg.CredentialRef)
	existing := syncops.BlankDoc(&unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Secret",
	}})
	getErr := cli.Get(ctx, client.ObjectKey{Namespace: cfg.Namespace, Name: name}, existing)
	switch {
	case getErr == nil:
	case apierrors.IsNotFound(getErr):
		existing = nil
	default:
		return fmt.Errorf("get secret %s/%s: %w", cfg.Namespace, name, getErr)
	}

	verb, action := "create", "create"
	if existing != nil {
		verb, action = "update", "update"
	}
	if !id.authorize(secretsResource, verb, cfg.Cluster, cfg.Namespace) {
		return fmt.Errorf("user does not have permission to %s %s in namespace %s on cluster %s",
			verb, secretsResource, cfg.Namespace, cfg.Cluster)
	}

	secret := existing
	if secret == nil {
		secret = &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": cfg.Namespace,
			},
		}}
	}
	secret.Object["type"] = pullSecretType
	secret.Object["data"] = data
	// A stale stringData would win over data server-side; the managed
	// secret's payload lives in data only.
	delete(secret.Object, "stringData")
	annotations := secret.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[store.AnnotationKiteManaged] = "true"
	secret.SetAnnotations(annotations)

	var applyErr error
	if existing == nil {
		applyErr = cli.Create(ctx, secret)
	} else {
		applyErr = cli.Update(ctx, secret)
	}
	h.recordOperation(id, store.HistoryEntry{
		Cluster: cfg.Cluster, ResourceType: secretsResource, ResourceName: name,
		Namespace: cfg.Namespace, Operation: action, YAML: redactedDoc(secret),
	}, applyErr == nil, errString(applyErr))
	if applyErr != nil {
		return fmt.Errorf("%s secret %s/%s: %w", action, cfg.Namespace, name, applyErr)
	}
	return nil
}

// ingressCertificateNames lists the certificate names the manifest set's
// Ingresses reference via the kite.application/certificate annotation, in
// first-seen order.
func ingressCertificateNames(docs []unstructured.Unstructured) []string {
	seen := map[string]bool{}
	var out []string
	for i := range docs {
		d := &docs[i]
		if d.GetKind() != "Ingress" {
			continue
		}
		name := d.GetAnnotations()[store.AnnotationKiteCertificate]
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// ensureTLSSecret upserts the managed kubernetes.io/tls secret of every
// certificate the manifest set's Ingresses reference; no annotated Ingress
// means no-op. A live secret whose cert-hash annotation already matches the
// certificate's content hash is left untouched — the skip path of an
// unrotated certificate. Like ensurePullSecret, the update path rewrites only
// type/data and the managed annotations and keeps foreign fields of the live
// secret.
func (h *Handler) ensureTLSSecret(ctx context.Context, id identity, cli client.Client, cfg *store.AppConfig, manifests []unstructured.Unstructured) error {
	for _, certName := range ingressCertificateNames(manifests) {
		certPEM, keyPEM, contentHash, err := h.deps.CertificateData(certName)
		if err != nil {
			return fmt.Errorf("resolve certificate %q: %w", certName, err)
		}
		name := render.TLSSecretName(certName)
		existing := syncops.BlankDoc(&unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1", "kind": "Secret",
		}})
		getErr := cli.Get(ctx, client.ObjectKey{Namespace: cfg.Namespace, Name: name}, existing)
		switch {
		case getErr == nil:
		case apierrors.IsNotFound(getErr):
			existing = nil
		default:
			return fmt.Errorf("get secret %s/%s: %w", cfg.Namespace, name, getErr)
		}
		if existing != nil && existing.GetAnnotations()[store.AnnotationKiteCertHash] == contentHash {
			continue
		}

		verb, action := "create", "create"
		if existing != nil {
			verb, action = "update", "update"
		}
		if !id.authorize(secretsResource, verb, cfg.Cluster, cfg.Namespace) {
			return fmt.Errorf("user does not have permission to %s %s in namespace %s on cluster %s",
				verb, secretsResource, cfg.Namespace, cfg.Cluster)
		}

		secret := existing
		if secret == nil {
			secret = &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Secret",
				"metadata": map[string]interface{}{
					"name":      name,
					"namespace": cfg.Namespace,
				},
			}}
		}
		secret.Object["type"] = tlsSecretType
		secret.Object["data"] = map[string]interface{}{
			"tls.crt": base64.StdEncoding.EncodeToString([]byte(certPEM)),
			"tls.key": base64.StdEncoding.EncodeToString([]byte(keyPEM)),
		}
		// A stale stringData would win over data server-side; the managed
		// secret's payload lives in data only.
		delete(secret.Object, "stringData")
		annotations := secret.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[store.AnnotationKiteManaged] = "true"
		annotations[store.AnnotationKiteCertHash] = contentHash
		secret.SetAnnotations(annotations)

		var applyErr error
		if existing == nil {
			applyErr = cli.Create(ctx, secret)
		} else {
			applyErr = cli.Update(ctx, secret)
		}
		h.recordOperation(id, store.HistoryEntry{
			Cluster: cfg.Cluster, ResourceType: secretsResource, ResourceName: name,
			Namespace: cfg.Namespace, Operation: action, YAML: redactedDoc(secret),
		}, applyErr == nil, errString(applyErr))
		if applyErr != nil {
			return fmt.Errorf("%s secret %s/%s: %w", action, cfg.Namespace, name, applyErr)
		}
	}
	return nil
}

// namespacedCopy returns a copy of doc with the config namespace applied;
// the config namespace is authoritative for every document (design §3).
func namespacedCopy(doc *unstructured.Unstructured, namespace string) (*unstructured.Unstructured, error) {
	if doc.GetKind() == "" || doc.GetAPIVersion() == "" {
		return nil, fmt.Errorf("document %s has no apiVersion/kind", store.DocKeyOf(doc))
	}
	out := doc.DeepCopy()
	if out.GetNamespace() == "" {
		out.SetNamespace(namespace)
	}
	return out, nil
}

// applyDoc creates or updates one document (design §6): RESTMapper scope →
// per-resource RBAC → ownership-checked create/recreate → Create/Update with
// resourceVersion, honoring dry-run. Returns the action taken.
func (h *Handler) applyDoc(ctx context.Context, id identity, cli client.Client, appName string, cfg *store.AppConfig, doc *unstructured.Unstructured, liveObj, anchorDoc *unstructured.Unstructured, dryRun bool) (string, error) {
	mapping, err := syncops.RestMapping(cli, doc)
	if err != nil {
		return "", err
	}
	if mapping.Scope.Name() == meta.RESTScopeNamespace.Name() {
		doc.SetNamespace(cfg.Namespace)
	}
	resource := mapping.Resource.Resource
	key := store.DocKeyOf(doc)

	if liveObj == nil {
		fresh := syncops.BlankDoc(doc)
		err := cli.Get(ctx, client.ObjectKey{Namespace: doc.GetNamespace(), Name: doc.GetName()}, fresh)
		switch {
		case apierrors.IsNotFound(err):
			// truly absent → create below
		case err != nil:
			return "", fmt.Errorf("get %s: %w", key, err)
		default:
			liveObj = fresh
		}
	}
	if liveObj != nil && anchorDoc == nil && liveObj.GetLabels()[store.LabelKiteName] != appName {
		// First publish / recreate onto an object the anchor does not know:
		// kite never silently takes over foreign objects (design §6).
		return "", fmt.Errorf("cluster object %s already exists and is not managed by this application", key)
	}

	verb, action := "update", "update"
	if liveObj == nil {
		verb, action = "create", "create"
	}
	if !id.authorize(resource, verb, cfg.Cluster, cfg.Namespace) {
		return "", fmt.Errorf("user does not have permission to %s %s in namespace %s on cluster %s",
			verb, resource, cfg.Namespace, cfg.Cluster)
	}

	var applyErr error
	if liveObj == nil {
		opts := &client.CreateOptions{}
		if dryRun {
			opts.DryRun = []string{metav1.DryRunAll}
		}
		applyErr = cli.Create(ctx, doc, opts)
	} else {
		doc.SetResourceVersion(liveObj.GetResourceVersion())
		opts := &client.UpdateOptions{}
		if dryRun {
			opts.DryRun = []string{metav1.DryRunAll}
		}
		applyErr = cli.Update(ctx, doc, opts)
	}
	h.recordOperation(id, store.HistoryEntry{
		Cluster: cfg.Cluster, ResourceType: resource, ResourceName: doc.GetName(),
		Namespace: doc.GetNamespace(), Operation: action, YAML: redactedDoc(doc),
	}, applyErr == nil, errString(applyErr))
	if applyErr != nil {
		return action, fmt.Errorf("%s %s: %w", action, key, applyErr)
	}
	return action, nil
}

// readBack fetches the applied object and normalizes it into the anchor
// (design §4.1); best-effort, the caller reports failures in the result.
func readBack(ctx context.Context, cli client.Client, doc *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	obj := syncops.BlankDoc(doc)
	if err := cli.Get(ctx, client.ObjectKey{Namespace: doc.GetNamespace(), Name: doc.GetName()}, obj); err != nil {
		return nil, fmt.Errorf("read back %s: %w", store.DocKeyOf(doc), err)
	}
	return syncops.Normalize(obj), nil
}

// persistDeployResult writes the anchor and the deploy bookkeeping in one
// transaction: read-backs of applied documents replace the previous anchor,
// failed documents keep theirs (design §6 读回锚定). On a fully successful
// publish appliedRev becomes the config's live revision (a 0 appliedRev is a
// bare republish of the stored set, i.e. the newest revision); a partial
// failure leaves the marker untouched — the cluster then matches no single
// revision until the error is republished.
func (h *Handler) persistDeployResult(cfg *store.AppConfig, previousAnchor, readBacks []unstructured.Unstructured, failedKeys map[string]bool, allOK bool, appliedRev int) error {
	var newAnchor []unstructured.Unstructured
	for i := range previousAnchor {
		if failedKeys[store.DocKeyOf(&previousAnchor[i])] {
			newAnchor = append(newAnchor, previousAnchor[i])
		}
	}
	newAnchor = append(newAnchor, readBacks...)
	anchorData, err := store.EncodeManifests(newAnchor)
	if err != nil {
		return fmt.Errorf("encode anchor: %w", err)
	}
	now := time.Now()
	summary := store.StatusSynced
	if !allOK {
		summary = store.StatusError
	}
	// last_deployed_at marks the last successful publish — a fully failed
	// attempt leaves it alone so the list doesn't show a publish time next
	// to an undeployed/error chip.
	updates := map[string]interface{}{
		"last_applied":      store.SecretJSON(anchorData),
		"status_summary":    summary,
		"status_checked_at": now,
	}
	if allOK {
		updates["last_deployed_at"] = now
	}
	return h.store.DB().Transaction(func(tx *gorm.DB) error {
		if allOK {
			if appliedRev == 0 {
				if err := tx.Model(&store.ConfigRevision{}).
					Where("app_config_id = ?", cfg.ID).
					Select("COALESCE(MAX(rev), 0)").
					Scan(&appliedRev).Error; err != nil {
					return err
				}
			}
			updates["applied_rev"] = appliedRev
		}
		return tx.Model(&store.AppConfig{}).Where("id = ?", cfg.ID).Updates(updates).Error
	})
}

// persistImage writes the requested image into the workload manifests and
// records the Source=deploy-image revision before the apply (design §6:
// 改镜像即发布 — the manifests change is persisted first, then applied whole).
func (h *Handler) persistImage(id identity, app *store.App, cfg *store.AppConfig, image string) ([]unstructured.Unstructured, int, error) {
	if strings.TrimSpace(image) == "" {
		return nil, 0, errors.New("image must not be empty")
	}
	previous := string(cfg.Manifests)
	var docs []unstructured.Unstructured
	newRev := 0
	txErr := h.store.DB().Transaction(func(tx *gorm.DB) error {
		cur, err := lockConfig(tx, cfg.ID)
		if err != nil {
			return err
		}
		docs, err = store.DecodeManifests(cur.Manifests)
		if err != nil {
			return err
		}
		wi := render.FindWorkload(app.Name, docs)
		if wi < 0 {
			return errors.New("config has no workload document to set the image on")
		}
		if !setImageOnWorkload(&docs[wi], image) {
			return errors.New("config workload has no container to set the image on")
		}
		data, err := store.EncodeManifests(docs)
		if err != nil {
			return err
		}
		if err := tx.Model(&store.AppConfig{}).Where("id = ?", cur.ID).
			Updates(map[string]interface{}{"manifests": store.SecretJSON(data)}).Error; err != nil {
			return err
		}
		newRev, err = store.AppendRevisionTx(tx, cur.ID, store.RevSourceDeployImage, data, id.operatorID, 0)
		if err != nil {
			return err
		}
		cfg.Manifests = data
		return nil
	})
	if txErr != nil {
		return nil, 0, txErr
	}
	h.recordAudit(id, store.HistoryEntry{
		Cluster: cfg.Cluster, ResourceType: authzResource, ResourceName: app.Name,
		Namespace: cfg.Namespace, Operation: "update",
		YAML:         redacted(cfg.Manifests),
		PreviousYAML: redacted(store.JSONField(previous)),
	})
	return docs, newRev, nil
}

// previewImage injects the requested image into an in-memory copy of the
// manifests so a dry-run validates the pending image without persisting it
// (design §6: dry-run touches neither cluster state nor the DB).
func previewImage(manifests []unstructured.Unstructured, appName, image string) ([]unstructured.Unstructured, error) {
	if strings.TrimSpace(image) == "" {
		return nil, errors.New("image must not be empty")
	}
	out := make([]unstructured.Unstructured, len(manifests))
	for i := range manifests {
		out[i] = *manifests[i].DeepCopy()
	}
	wi := render.FindWorkload(appName, out)
	if wi < 0 {
		return nil, errors.New("config has no workload document to set the image on")
	}
	if !setImageOnWorkload(&out[wi], image) {
		return nil, errors.New("config workload has no container to set the image on")
	}
	return out, nil
}

// setImageOnWorkload sets the image on the form-managed container, mutating
// the document in place.
func setImageOnWorkload(doc *unstructured.Unstructured, image string) bool {
	specMap, ok := store.AsMap(doc.Object["spec"])
	if !ok {
		return false
	}
	tpl, ok := store.AsMap(specMap["template"])
	if !ok {
		return false
	}
	podSpec, ok := store.AsMap(tpl["spec"])
	if !ok {
		return false
	}
	containers, ok := store.AsList(podSpec["containers"])
	if !ok {
		return false
	}
	idx := render.PrimaryContainerIndex(containers)
	if idx < 0 {
		return false
	}
	container, ok := store.AsMap(containers[idx])
	if !ok {
		return false
	}
	container["image"] = image
	return true
}

// redactedDocRaw masks Secret values of one document for API responses
// (design §9); the result is the redacted JSON object itself.
func redactedDocRaw(doc *unstructured.Unstructured) json.RawMessage {
	data, err := json.Marshal(doc.Object)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(redactJSON(data))
}

func redactedDoc(doc *unstructured.Unstructured) string {
	data, err := json.Marshal(doc.Object)
	if err != nil {
		return "\"<redacted>\""
	}
	return string(redactJSON(data))
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// docMap indexes a document set by docKey.
func docMap(docs []unstructured.Unstructured) map[string]*unstructured.Unstructured {
	out := make(map[string]*unstructured.Unstructured, len(docs))
	for i := range docs {
		out[store.DocKeyOf(&docs[i])] = &docs[i]
	}
	return out
}
