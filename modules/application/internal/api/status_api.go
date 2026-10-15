package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/application/internal/render"
	"github.com/funcx27/kite/modules/application/internal/runtime"
	"github.com/funcx27/kite/modules/application/internal/store"
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

// statusDoc is one status diff entry: the change set plus the redacted
// normalized live/manifest views for side-by-side display and adoptions. A
// document missing on one side omits that side's view.
type statusDoc struct {
	syncops.DocDiff
	Live     json.RawMessage `json:"live,omitempty"`
	Manifest json.RawMessage `json:"manifest,omitempty"`
	// Note explains a synthetic entry that carries no document diff — the
	// certificate rotation notices below.
	Note string `json:"note,omitempty"`
}

type statusResponse struct {
	Status string      `json:"status"`
	Docs   []statusDoc `json:"docs"`
	// PreviewToken is the stable hash of the diff behind this report; the
	// resolve endpoint requires it back.
	PreviewToken string    `json:"previewToken,omitempty"`
	CheckedAt    time.Time `json:"checkedAt"`
	Error        string    `json:"error,omitempty"`
	// ReadyReplicas/Replicas are the live workload's replica counts, persisted
	// with the check for the list page's readiness display; nil when the app
	// has no workload.
	ReadyReplicas *int64 `json:"readyReplicas,omitempty"`
	Replicas      *int64 `json:"replicas,omitempty"`
}

// GetStatus computes the single-channel drift report: the diff between the
// stored manifests and the live cluster objects, and writes the outcome back
// to StatusSummary/StatusCheckedAt plus the workload's replica counts so the
// list page aggregates the persisted values without re-diffing (design
// §3/§11). A failed detection is itself a check outcome: the config is marked
// error and the message is returned in the body. Configs without manifests
// answer undeployed from the DB alone, so a down cluster never blocks their
// report.
func (h *Handler) GetStatus(c *gin.Context) {
	app, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	if !h.authorize(c, "get", cfg.Cluster, cfg.Namespace) {
		return
	}
	now := time.Now()

	resp, checkErr := h.computeStatus(c, app.Name, cfg)
	resp.CheckedAt = now
	updates := map[string]interface{}{
		"status_summary":    resp.Status,
		"status_checked_at": now,
	}
	// A completed check refreshes the replica counts (nil clears them, e.g.
	// undeployed or no workload); a failed one keeps the last known values.
	if resp.Status != store.StatusError {
		updates["ready_replicas"] = resp.ReadyReplicas
		updates["replicas"] = resp.Replicas
	}
	if err := h.store.DB().Model(&store.AppConfig{}).Where("id = ?", cfg.ID).Updates(updates).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if checkErr != nil {
		resp.Error = checkErr.Error()
	}
	c.JSON(http.StatusOK, resp)
}

// computeStatus runs the drift check for one config: the live objects are
// only fetched when there are manifests to compare them against. Both diff
// sides are normalized inside DiffManifests, so server-managed fields never
// surface as drift.
func (h *Handler) computeStatus(c *gin.Context, appName string, cfg *store.AppConfig) (statusResponse, error) {
	manifests, err := store.DecodeManifests(cfg.Manifests)
	if err != nil {
		return errorStatus(fmt.Errorf("decode manifests: %w", err))
	}
	if len(manifests) == 0 {
		return statusResponse{Status: store.StatusUndeployed, Docs: []statusDoc{}}, nil
	}
	cli, err := h.deps.ResolveCluster(c.Request.Context(), cfg.Cluster)
	if err != nil {
		return errorStatus(fmt.Errorf("resolve cluster %s: %w", cfg.Cluster, err))
	}
	live, err := syncops.FetchLiveDocs(c.Request.Context(), cli, syncops.IdentityTemplates(manifests, nil))
	if err != nil {
		return errorStatus(fmt.Errorf("fetch live objects: %w", err))
	}
	anchor, err := store.DecodeManifests(cfg.LastApplied)
	if err != nil {
		return errorStatus(fmt.Errorf("decode anchor: %w", err))
	}
	resp := statusOf(manifests, anchor, live)
	resp.ReadyReplicas, resp.Replicas = workloadReplicas(appName, manifests, live)
	if err := h.noteCertificateRotation(c, cli, cfg, manifests, &resp); err != nil {
		return errorStatus(err)
	}
	return resp, nil
}

// noteCertificateRotation reports managed TLS secrets that no longer match
// their platform certificate: the cert-hash annotation of the live secret is
// compared against the certificate's current content hash. A mismatch means
// the certificate was rotated and the config awaits a publish, so an
// otherwise synced report is downgraded to pending; existing drift/pending
// verdicts stand unchanged. A certificate that fails to resolve (deleted or
// unreadable) becomes a rotation note instead of failing the whole check.
func (h *Handler) noteCertificateRotation(c *gin.Context, cli client.Client, cfg *store.AppConfig, manifests []unstructured.Unstructured, resp *statusResponse) error {
	for _, certName := range ingressCertificateNames(manifests) {
		name := render.TLSSecretName(certName)
		hash, err := h.deps.CertificateHash(certName)
		if err != nil {
			resp.appendRotationNote(cfg.Namespace, name,
				fmt.Sprintf("certificate %q is unavailable: %v", certName, err))
			continue
		}
		existing := syncops.BlankDoc(&unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1", "kind": "Secret",
		}})
		getErr := cli.Get(c.Request.Context(), client.ObjectKey{Namespace: cfg.Namespace, Name: name}, existing)
		switch {
		case getErr == nil:
			if existing.GetAnnotations()[store.AnnotationKiteCertHash] == hash {
				continue
			}
			resp.appendRotationNote(cfg.Namespace, name,
				fmt.Sprintf("certificate %q has been updated; publish to rotate the managed TLS secret", certName))
		case apierrors.IsNotFound(getErr):
			resp.appendRotationNote(cfg.Namespace, name,
				fmt.Sprintf("managed TLS secret of certificate %q is missing; publish to create it", certName))
		default:
			return fmt.Errorf("get secret %s/%s: %w", cfg.Namespace, name, getErr)
		}
	}
	return nil
}

// appendRotationNote records one certificate rotation finding: synced
// configs drop to pending (the cluster no longer runs the stored
// certificate's material), any other verdict keeps its original judgment.
func (resp *statusResponse) appendRotationNote(namespace, secretName, note string) {
	if resp.Status == store.StatusSynced {
		resp.Status = store.StatusPending
	}
	resp.Docs = append(resp.Docs, statusDoc{
		DocDiff: syncops.DocDiff{Kind: "Secret", Namespace: namespace, Name: secretName},
		Note:    note,
	})
}

// workloadReplicas reads the live workload's replica counts for the persisted
// readiness display; nil when the manifests define no workload or the cluster
// object is gone.
func workloadReplicas(app string, manifests []unstructured.Unstructured, live map[string]*unstructured.Unstructured) (ready, replicas *int64) {
	wl := runtime.WorkloadOf(app, manifests)
	if wl == nil {
		return nil, nil
	}
	lw := live[store.DocKey(wl.GetKind(), wl.GetNamespace(), wl.GetName())]
	if lw == nil {
		return nil, nil
	}
	readyCount, _, _ := unstructured.NestedInt64(lw.Object, "status", "readyReplicas")
	// spec.replicas absent means 1, the API server default (runtime's
	// defaultReplicas).
	desired, found, _ := unstructured.NestedInt64(lw.Object, "spec", "replicas")
	if !found {
		desired = 1
	}
	return &readyCount, &desired
}

// statusOf builds the status response for one DB/anchor/live snapshot:
// synced when the DB manifests match the cluster, pending when only the DB
// side moved (saved-but-unpublished edits — the cluster still matches the
// last publish's anchor), drift when the cluster moved off the anchor.
func statusOf(manifests, anchor []unstructured.Unstructured, live map[string]*unstructured.Unstructured) statusResponse {
	diff := syncops.DiffManifests(manifests, syncops.LiveList(live, manifests))
	status := store.StatusDrift
	switch {
	case len(diff) == 0:
		status = store.StatusSynced
	case len(syncops.DiffManifests(anchor, syncops.LiveList(live, anchor))) == 0:
		status = store.StatusPending
	}
	return statusResponse{
		Status:       status,
		Docs:         buildStatusDocs(diff, manifests, live),
		PreviewToken: previewTokenHash(diff),
	}
}

func errorStatus(err error) (statusResponse, error) {
	return statusResponse{Status: store.StatusError, Docs: []statusDoc{}}, err
}

// buildStatusDocs pairs each diff entry with the redacted normalized live and
// manifest documents — the same views the diff paths refer to (Normalize
// folds Secret stringData into data, redactedDocRaw masks those values).
func buildStatusDocs(diff []syncops.DocDiff, manifests []unstructured.Unstructured, live map[string]*unstructured.Unstructured) []statusDoc {
	manIdx := docMap(manifests)
	out := make([]statusDoc, 0, len(diff))
	for i := range diff {
		doc := statusDoc{DocDiff: diff[i]}
		key := store.DocKey(diff[i].Kind, diff[i].Namespace, diff[i].Name)
		var liveDoc, manDoc *unstructured.Unstructured
		if ld := live[key]; ld != nil {
			liveDoc = syncops.Normalize(ld)
		}
		if md := manIdx[key]; md != nil {
			manDoc = syncops.Normalize(md)
		}
		// Same normalization the diff ran on, defaulted pull policy included,
		// so the views never show a line the diff paths do not carry.
		if liveDoc != nil && manDoc != nil {
			syncops.AbsorbDefaultedPullPolicy(manDoc, liveDoc)
		}
		if liveDoc != nil {
			doc.Live = redactedDocRaw(liveDoc)
		}
		if manDoc != nil {
			doc.Manifest = redactedDocRaw(manDoc)
		}
		out = append(out, doc)
	}
	return out
}
