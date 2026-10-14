package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/funcx27/kite/modules/application/internal/store"
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

// managedKinds are the kinds the module renders and adopts; sync/offline only
// touch these plus the explicit manifest identities (design §6).
var managedKinds = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
	"Service":     true,
	"Ingress":     true,
	"ConfigMap":   true,
	"Secret":      true,
}

var kindResources = map[string]string{
	"Deployment":  "deployments",
	"StatefulSet": "statefulsets",
	"Service":     "services",
	"Ingress":     "ingresses",
	"ConfigMap":   "configmaps",
	"Secret":      "secrets",
}

type syncResponse struct {
	Success   bool     `json:"success"`
	Adopted   []string `json:"adopted"` // documents taken from the cluster
	Kept      []string `json:"kept"`    // non-managed documents preserved verbatim (ExtraYAML)
	Skipped   []string `json:"skipped"` // documents not adopted for missing read permission
	LatestRev int      `json:"latestRev"`
}

// SyncConfig adopts the cluster state into the DB (design §6 拉取): the read
// set is the manifest/anchor identities plus every managed-kind document
// carrying the app's identity label; managed kinds are replaced by their
// normalized cluster state, other documents (ExtraYAML kinds the module
// cannot reconcile) are preserved verbatim, and both snapshots are
// overwritten with one Source=sync revision.
func (h *Handler) SyncConfig(c *gin.Context) {
	app, cfg, ok := h.findAppConfig(c)
	if !ok {
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
	labeled, err := syncops.ListManagedByLabel(c.Request.Context(), cli, app.Name, cfg.Namespace)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	adoptSource := func(key string) *unstructured.Unstructured {
		if d, ok := live[key]; ok {
			return d
		}
		return labeled[key]
	}

	var adopted, kept, skipped []string
	var next []unstructured.Unstructured
	for i := range manifests {
		doc := &manifests[i]
		key := store.DocKeyOf(doc)
		if !managedKinds[doc.GetKind()] {
			kept = append(kept, key)
			next = append(next, *doc.DeepCopy())
			continue
		}
		clusterDoc := adoptSource(key)
		if clusterDoc == nil {
			// Deleted on the cluster: the adoption includes the deletion.
			adopted = append(adopted, key)
			continue
		}
		if !h.deps.Authorize(c, kindResource(doc.GetKind()), "get", cfg.Cluster, cfg.Namespace) {
			// No read permission: the DB-side configuration is kept verbatim
			// so an adoption never silently drops a document (design §1:
			// 拉取 = 集群→DB 无损收编).
			skipped = append(skipped, key)
			next = append(next, *doc.DeepCopy())
			continue
		}
		adopted = append(adopted, key)
		next = append(next, *syncops.Normalize(clusterDoc))
	}
	for key, d := range labeled {
		if _, already := live[key]; already {
			continue
		}
		if !h.deps.Authorize(c, kindResource(d.GetKind()), "get", cfg.Cluster, cfg.Namespace) {
			skipped = append(skipped, key)
			continue
		}
		adopted = append(adopted, key)
		next = append(next, *syncops.Normalize(d))
	}
	if len(next) == 0 {
		h.fail(c, http.StatusBadRequest, nil, "sync found no documents to adopt; the cluster has none of this app's objects")
		return
	}

	data, err := store.EncodeManifests(next)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	now := time.Now()
	newRev := 0
	txErr := h.store.DB().Transaction(func(tx *gorm.DB) error {
		cur, err := lockConfig(tx, cfg.ID)
		if err != nil {
			return err
		}
		// The revision lands first so applied_rev can name it: the sync
		// revision's content is what the cluster now runs.
		newRev, err = store.AppendRevisionTx(tx, cur.ID, store.RevSourceSync, data, h.deps.OperatorID(c), 0)
		if err != nil {
			return err
		}
		return tx.Model(&store.AppConfig{}).Where("id = ?", cur.ID).Updates(map[string]interface{}{
			"manifests":         store.SecretJSON(data),
			"last_applied":      store.SecretJSON(data),
			"applied_rev":       newRev,
			"status_summary":    store.StatusSynced,
			"status_checked_at": now,
			"last_synced_at":    now,
		}).Error
	})
	if txErr != nil {
		h.fail(c, http.StatusInternalServerError, txErr, "")
		return
	}
	h.recordAudit(h.requestIdentity(c), store.HistoryEntry{
		Cluster: cfg.Cluster, ResourceType: authzResource, ResourceName: app.Name,
		Namespace: cfg.Namespace, Operation: "update", YAML: redacted(data),
		PreviousYAML: redacted(cfg.Manifests),
	})
	c.JSON(http.StatusOK, syncResponse{
		Success:   true,
		Adopted:   adopted,
		Kept:      kept,
		Skipped:   skipped,
		LatestRev: newRev,
	})
}

// kindResource maps a managed kind to its plural resource name (kite RBAC
// resources are lowercase plurals, see pkg/rbac).
func kindResource(kind string) string {
	if r, ok := kindResources[kind]; ok {
		return r
	}
	return fmt.Sprintf("%ss", strings.ToLower(kind))
}
