package api

import (
	"context"
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
	Results     []deployDocResult `json:"results"`
	Anchored    bool              `json:"anchored"`
	AnchorError string            `json:"anchorError,omitempty"`
	Drift       []syncops.DocDiff `json:"drift,omitempty"`   // diff(live, anchor): cluster-side drift
	Pending     []syncops.DocDiff `json:"pending,omitempty"` // diff(manifests, anchor): DB-side pending
	Change      []syncops.DocDiff `json:"change,omitempty"`  // diff(live, manifests): net change to push
	// Preview carries the redacted old/new document pair per changed document
	// so the UI can render a YAML diff of the net change (design §6).
	Preview   []deployDocPreview `json:"preview,omitempty"`
	LatestRev int                `json:"latestRev,omitempty"`
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
// the conflict gate first (every cluster-side drift path must be merged or
// knowingly overridden), then ensure-namespace, then per-document
// Create/Update with per-resource RBAC, ownership-checked creates and a
// best-effort anchor read-back. {dryRun:true} runs the whole pipeline against
// the API server with dry-run: All and touches neither cluster state beyond
// reads nor the database. Pending changes ({spec}/{manifests}) and {image}
// are persisted as a revision and applied by this same request — the save
// never happens without the publish.
func (h *Handler) DeployConfig(c *gin.Context) {
	app, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	var req deployRequest
	if err := bindOptionalJSON(c, &req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	if req.Spec != nil && req.Manifests != nil {
		h.fail(c, http.StatusBadRequest, nil, "provide either spec or manifests, not both")
		return
	}
	cli, err := h.deps.ResolveCluster(c.Request.Context(), cfg.Cluster)
	if err != nil {
		h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+cfg.Cluster)
		return
	}
	stored, err := store.DecodeManifests(cfg.Manifests)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if len(stored) == 0 {
		h.fail(c, http.StatusBadRequest, nil, "config has no manifests to deploy")
		return
	}

	var (
		manifests []unstructured.Unstructured
		latestRev int
	)
	switch {
	case req.Spec != nil:
		var spec render.AppSpec
		if err := json.Unmarshal(req.Spec, &spec); err != nil {
			h.fail(c, http.StatusBadRequest, nil, "spec must be a valid AppSpec JSON object: "+err.Error())
			return
		}
		spec.Normalize(app.Name)
		if err := spec.Validate(); err != nil {
			h.fail(c, http.StatusBadRequest, nil, "invalid spec: "+err.Error())
			return
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
			return render.InjectFields(app.Name, cur.Namespace, curDocs, spec)
		}
		if req.DryRun {
			manifests, err = render.InjectFields(app.Name, cfg.Namespace, stored, spec)
			if err != nil {
				h.fail(c, http.StatusBadRequest, err, "")
				return
			}
			restoreMaskedSecretValues(manifests, stored)
		} else {
			manifests, latestRev, err = h.persistPendingConfig(c, app, cfg, req.ExpectedRev, store.RevSourceWizard, build, req.Image)
			if !h.failPendingPersist(c, err) {
				return
			}
		}
	case req.Manifests != nil:
		incoming, err := store.DecodeManifests(req.Manifests)
		if err != nil {
			h.fail(c, http.StatusBadRequest, err, "")
			return
		}
		build := func(cur *store.AppConfig, curDocs []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
			return incoming, nil
		}
		if req.DryRun {
			manifests = incoming
			restoreMaskedSecretValues(manifests, stored)
		} else {
			manifests, latestRev, err = h.persistPendingConfig(c, app, cfg, req.ExpectedRev, store.RevSourceYAML, build, req.Image)
			if !h.failPendingPersist(c, err) {
				return
			}
		}
	default:
		if req.Image != "" && req.DryRun {
			// Dry-run must validate the pending image without persisting it
			// (design §6: dry-run touches neither cluster state nor the DB).
			manifests, err = previewImage(stored, app.Name, req.Image)
			if err != nil {
				h.fail(c, http.StatusBadRequest, err, "")
				return
			}
		} else if req.Image != "" {
			manifests, latestRev, err = h.persistImage(c, app, cfg, req.Image)
			if err != nil {
				h.fail(c, http.StatusBadRequest, err, "")
				return
			}
		} else {
			manifests = stored
		}
	}
	// A dialog image edit on top of a pending set rides the same request and
	// the same revision; the non-dry-run path applied it inside the persist.
	if req.Image != "" && (req.Spec != nil || req.Manifests != nil) && req.DryRun {
		if err := setImageOnPending(manifests, app.Name, req.Image); err != nil {
			h.fail(c, http.StatusBadRequest, err, "")
			return
		}
	}

	resp, conflicts, err := h.runPublish(c, app, cfg, cli, manifests, req.DryRun)
	var nf *namespaceForbiddenError
	switch {
	case errors.As(err, &nf):
		h.fail(c, http.StatusForbidden, err, err.Error())
		return
	case err != nil:
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if len(conflicts) > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error":     "cluster has manual changes conflicting with the pending publish, merge first",
			"conflicts": conflicts,
		})
		return
	}
	resp.LatestRev = latestRev
	c.JSON(http.StatusOK, resp)
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

// runPublish runs the conflict-gated apply pipeline for the pending manifest
// set (design §6): ensure-namespace, live fetch, the gate, per-document
// Create/Update and the anchor read-back; it ends with the drift/pending/
// change diff groups. A gate verdict is returned as conflicts without any
// write; with dryRun the apply only validates against the API server.
func (h *Handler) runPublish(c *gin.Context, app *store.App, cfg *store.AppConfig, cli client.Client, manifests []unstructured.Unstructured, dryRun bool) (deployResponse, []syncops.ConflictDetail, error) {
	if !dryRun {
		if err := h.ensureNamespace(c, cli, cfg.Cluster, cfg.Namespace); err != nil {
			return deployResponse{}, nil, err
		}
	}

	anchor, err := store.DecodeManifests(cfg.LastApplied)
	if err != nil {
		return deployResponse{}, nil, err
	}
	templates := syncops.IdentityTemplates(manifests, anchor)
	live, err := syncops.FetchLiveDocs(c.Request.Context(), cli, templates)
	if err != nil {
		return deployResponse{}, nil, err
	}

	// The gate and the diff groups run on normalized live objects: raw ones
	// carry server-managed fields (status, clusterIP, last-applied
	// annotations) that would surface as fake cluster-side drift. The apply
	// path below keeps the raw objects — Update needs the resourceVersion.
	liveForDiff := make(map[string]*unstructured.Unstructured, len(live))
	for key, doc := range live {
		liveForDiff[key] = syncops.Normalize(doc)
	}

	if len(anchor) > 0 {
		overridden := decodeScopedPaths(cfg.OverriddenPaths)
		if conflicts := syncops.ConflictGate(manifests, anchor, liveForDiff, overridden); len(conflicts) > 0 {
			return deployResponse{}, conflicts, nil
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
		action, applyErr := h.applyDoc(c, cli, app.Name, cfg, doc, live[key], anchorByKey[key], dryRun)
		result.Action = action
		if applyErr != nil {
			result.Success, result.Error = false, applyErr.Error()
			failedKeys[key] = true
		} else if !dryRun {
			rb, rbErr := readBack(c.Request.Context(), cli, doc)
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
		if err := h.persistDeployResult(c, cfg, anchor, anchorReadBacks, failedKeys, allOK, anchorErr); err != nil {
			return deployResponse{}, nil, err
		}
	}

	drift := syncops.DiffManifests(syncops.LiveList(liveForDiff, anchor), anchor)
	pending := syncops.DiffManifests(manifests, anchor)
	change := syncops.DiffManifests(syncops.LiveList(liveForDiff, manifests), manifests)
	return deployResponse{
		Success:     allOK,
		DryRun:      dryRun,
		Results:     results,
		Anchored:    anchorErr == "" && !dryRun,
		AnchorError: anchorErr,
		Drift:       drift,
		Pending:     pending,
		Change:      change,
		Preview:     previewDocs(manifests, liveForDiff, change),
	}, nil, nil
}

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
			p.Old = redactedDocRaw(old)
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
// markers are restored from the current values, and the knowing-override
// whitelist does not survive a wholesale rewrite.
func (h *Handler) persistPendingConfig(
	c *gin.Context,
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
			Updates(map[string]interface{}{
				"manifests":        store.SecretJSON(data),
				"overridden_paths": store.JSONField(nil),
			}).Error; err != nil {
			return err
		}
		cfg.OverriddenPaths = store.JSONField(nil)
		cfg.Manifests = store.SecretJSON(data)
		newRev, err = store.AppendRevisionTx(tx, cur.ID, source, data, h.deps.OperatorID(c), 0)
		return err
	})
	if txErr != nil {
		return nil, 0, txErr
	}
	h.recordAudit(c, store.HistoryEntry{
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
func (h *Handler) ensureNamespace(c *gin.Context, cli client.Client, cluster, namespace string) error {
	existing := syncops.BlankDoc(&unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Namespace",
	}})
	existing.SetName(namespace)
	err := cli.Get(c.Request.Context(), client.ObjectKey{Name: namespace}, existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get namespace %s: %w", namespace, err)
	}
	if !h.deps.Authorize(c, "namespaces", "create", cluster, "") {
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
	if err := cli.Create(c.Request.Context(), ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", namespace, err)
	}
	h.recordAudit(c, store.HistoryEntry{
		Cluster: cluster, ResourceType: "namespaces", ResourceName: namespace, Operation: "create",
	})
	return nil
}

// decodeScopedPaths parses the stored OverriddenPaths list.
func decodeScopedPaths(data store.JSONField) map[string]bool {
	out := map[string]bool{}
	if len(data) == 0 {
		return out
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return out
	}
	for _, s := range list {
		out[s] = true
	}
	return out
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
func (h *Handler) applyDoc(c *gin.Context, cli client.Client, appName string, cfg *store.AppConfig, doc *unstructured.Unstructured, liveObj, anchorDoc *unstructured.Unstructured, dryRun bool) (string, error) {
	mapping, err := syncops.RestMapping(cli, doc)
	if err != nil {
		return "", err
	}
	if mapping.Scope.Name() == meta.RESTScopeNamespace.Name() {
		doc.SetNamespace(cfg.Namespace)
	}
	resource := mapping.Resource.Resource
	key := store.DocKeyOf(doc)
	ctx := c.Request.Context()

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
	if !h.deps.Authorize(c, resource, verb, cfg.Cluster, cfg.Namespace) {
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
	h.recordOperation(c, store.HistoryEntry{
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
// failed documents keep theirs (design §6 读回锚定). The override whitelist is
// consumed only by a fully successful publish.
func (h *Handler) persistDeployResult(c *gin.Context, cfg *store.AppConfig, previousAnchor, readBacks []unstructured.Unstructured, failedKeys map[string]bool, allOK bool, anchorErr string) error {
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
		updates["overridden_paths"] = store.JSONField(nil)
	}
	return h.store.DB().Transaction(func(tx *gorm.DB) error {
		return tx.Model(&store.AppConfig{}).Where("id = ?", cfg.ID).Updates(updates).Error
	})
}

// persistImage writes the requested image into the workload manifests and
// records the Source=deploy-image revision before the apply (design §6:
// 改镜像即发布 — the manifests change is persisted first, then applied whole).
func (h *Handler) persistImage(c *gin.Context, app *store.App, cfg *store.AppConfig, image string) ([]unstructured.Unstructured, int, error) {
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
		newRev, err = store.AppendRevisionTx(tx, cur.ID, store.RevSourceDeployImage, data, h.deps.OperatorID(c), 0)
		if err != nil {
			return err
		}
		cfg.Manifests = data
		return nil
	})
	if txErr != nil {
		return nil, 0, txErr
	}
	h.recordAudit(c, store.HistoryEntry{
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
