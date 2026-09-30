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
	"github.com/funcx27/kite/modules/application/internal/store"
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

type deployRequest struct {
	DryRun bool   `json:"dryRun"`
	Image  string `json:"image,omitempty"`
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
	LatestRev   int               `json:"latestRev,omitempty"`
}

// DeployConfig pushes the stored manifests to the cluster (design §6): the
// conflict gate first (every cluster-side drift path must be merged or
// knowingly overridden), then ensure-namespace, then per-document
// Create/Update with per-resource RBAC, ownership-checked creates and a
// best-effort anchor read-back. {dryRun:true} runs the whole pipeline against
// the API server with dry-run: All and touches neither cluster state beyond
// reads nor the database. {image} persists the image change first
// (Source=deploy-image) and deploys it with the rest; combined with
// {dryRun:true} the image is only injected in memory for validation.
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
	cli, err := h.deps.ResolveCluster(c.Request.Context(), cfg.Cluster)
	if err != nil {
		h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+cfg.Cluster)
		return
	}
	manifests, err := store.DecodeManifests(cfg.Manifests)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if len(manifests) == 0 {
		h.fail(c, http.StatusBadRequest, nil, "config has no manifests to deploy")
		return
	}

	latestRev := 0
	if req.Image != "" && req.DryRun {
		// Dry-run must validate the pending image without persisting it
		// (design §6: dry-run touches neither cluster state nor the DB).
		manifests, err = previewImage(manifests, app.Name, req.Image)
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
	}

	if !req.DryRun {
		if err := h.ensureNamespace(c, cli, cfg.Cluster, cfg.Namespace); err != nil {
			if errorsIsNamespaceForbidden(err) {
				h.fail(c, http.StatusForbidden, nil, err.Error())
				return
			}
			h.fail(c, http.StatusInternalServerError, err, "")
			return
		}
	}

	anchor, err := store.DecodeManifests(cfg.LastApplied)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	templates := syncops.IdentityTemplates(manifests, anchor)
	live, err := syncops.FetchLiveDocs(c.Request.Context(), cli, templates)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
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
			c.JSON(http.StatusConflict, gin.H{
				"error":     "cluster has manual changes conflicting with the pending publish, merge first",
				"conflicts": conflicts,
			})
			return
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
		action, applyErr := h.applyDoc(c, cli, app.Name, cfg, doc, live[key], anchorByKey[key], req.DryRun)
		result.Action = action
		if applyErr != nil {
			result.Success, result.Error = false, applyErr.Error()
			failedKeys[key] = true
		} else if !req.DryRun {
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
	if !req.DryRun {
		if err := h.persistDeployResult(c, cfg, anchor, anchorReadBacks, failedKeys, allOK, anchorErr); err != nil {
			h.fail(c, http.StatusInternalServerError, err, "")
			return
		}
	}

	drift := syncops.DiffManifests(syncops.LiveList(liveForDiff, anchor), anchor)
	pending := syncops.DiffManifests(manifests, anchor)
	change := syncops.DiffManifests(syncops.LiveList(liveForDiff, manifests), manifests)
	c.JSON(http.StatusOK, deployResponse{
		Success:     allOK,
		DryRun:      req.DryRun,
		Results:     results,
		Anchored:    anchorErr == "" && !req.DryRun,
		AnchorError: anchorErr,
		Drift:       drift,
		Pending:     pending,
		Change:      change,
		LatestRev:   latestRev,
	})
}

// namespaceForbiddenError marks a failed ensure-namespace permission check so
// the caller can answer 403 instead of 500.
type namespaceForbiddenError struct{ msg string }

func (e *namespaceForbiddenError) Error() string { return e.msg }

func errorsIsNamespaceForbidden(err error) bool {
	var nf *namespaceForbiddenError
	return errors.As(err, &nf)
}

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
		newRev, err = store.AppendRevisionTx(tx, cur.ID, store.RevSourceDeployImage, data, h.deps.OperatorID(c))
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
