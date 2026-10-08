package api

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/application/internal/store"
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

type offlineFailure struct {
	Doc   string `json:"doc"`
	Error string `json:"error"`
}

type offlineResponse struct {
	Success bool             `json:"success"`
	Deleted []string         `json:"deleted"`
	Missing []string         `json:"missing"`
	Failed  []offlineFailure `json:"failed,omitempty"`
	// ClusterGone marks the gone-cluster path: nothing was reachable to
	// delete, every manifest was counted missing without a cluster call.
	ClusterGone   bool `json:"clusterGone,omitempty"`
	ConfigDeleted bool `json:"configDeleted"`
	AppUndeployed bool `json:"appUndeployed"`
}

// OfflineConfig takes the app's cluster config down (design §6 下线): cluster
// objects are deleted strictly along the manifest list (render output plus
// ExtraYAML, no label sweep), then the config row and its revision chain are
// removed — 下线 is the one irreversible action, there is no keep-config
// variant. A partial cluster deletion keeps the config for retry (409).
func (h *Handler) OfflineConfig(c *gin.Context) {
	app, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	manifests, err := store.DecodeManifests(cfg.Manifests)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}

	resp := offlineResponse{Success: true, Deleted: []string{}, Missing: []string{}, Failed: []offlineFailure{}}
	cli, resolveErr := h.deps.ResolveCluster(c.Request.Context(), cfg.Cluster)
	if resolveErr != nil {
		// The cluster is gone from the platform, so nothing is reachable to
		// delete: every manifest counts as missing and only the config
		// bookkeeping runs — otherwise a deleted cluster would strand its
		// configs online forever.
		if !h.authorize(c, "delete", cfg.Cluster, cfg.Namespace) {
			return
		}
		resp.ClusterGone = true
		for i := range manifests {
			resp.Missing = append(resp.Missing, store.DocKeyOf(&manifests[i]))
		}
		h.finalizeOffline(c, app, cfg, resp)
		return
	}
	h.deleteClusterObjects(c, cfg, cli, manifests, &resp)
	if !resp.Success {
		// Never delete the config (irreversible) while cluster objects may
		// remain; surface the partial failure instead.
		h.fail(c, http.StatusConflict, nil, "cluster deletion incomplete, config kept for retry")
		return
	}
	h.finalizeOffline(c, app, cfg, resp)
}

// deleteClusterObjects deletes every manifest along the config's list, filling
// resp's deleted/missing/failed groups; the response's Success flag doubles as
// the all-deleted verdict.
func (h *Handler) deleteClusterObjects(c *gin.Context, cfg *store.AppConfig, cli client.Client, manifests []unstructured.Unstructured, resp *offlineResponse) {
	for i := range manifests {
		doc, err := namespacedCopy(&manifests[i], cfg.Namespace)
		key := store.DocKeyOf(doc)
		if err != nil {
			resp.Success = false
			resp.Failed = append(resp.Failed, offlineFailure{Doc: key, Error: err.Error()})
			continue
		}
		mapping, err := syncops.RestMapping(cli, doc)
		if err != nil {
			resp.Success = false
			resp.Failed = append(resp.Failed, offlineFailure{Doc: key, Error: err.Error()})
			continue
		}
		ns := cfg.Namespace
		if mapping.Scope.Name() != meta.RESTScopeNamespace.Name() {
			ns = ""
		}
		if !h.deps.Authorize(c, mapping.Resource.Resource, "delete", cfg.Cluster, ns) {
			resp.Success = false
			resp.Failed = append(resp.Failed, offlineFailure{Doc: key, Error: fmt.Sprintf(
				"user does not have permission to delete %s in namespace %s on cluster %s",
				mapping.Resource.Resource, ns, cfg.Cluster)})
			continue
		}
		delErr := cli.Delete(c.Request.Context(), doc)
		switch {
		case delErr == nil:
			resp.Deleted = append(resp.Deleted, key)
		case apierrors.IsNotFound(delErr):
			resp.Missing = append(resp.Missing, key)
		default:
			resp.Success = false
			resp.Failed = append(resp.Failed, offlineFailure{Doc: key, Error: delErr.Error()})
		}
		h.recordOperation(h.requestIdentity(c), store.HistoryEntry{
			Cluster: cfg.Cluster, ResourceType: mapping.Resource.Resource, ResourceName: doc.GetName(),
			Namespace: doc.GetNamespace(), Operation: "delete", YAML: redactedDoc(doc),
		}, delErr == nil || apierrors.IsNotFound(delErr), errString(delErr))
	}
}

// finalizeOffline purges the config row — only reachable when every cluster
// deletion succeeded or the cluster is gone — and reports the app's undeployed
// state from the remaining config count.
func (h *Handler) finalizeOffline(c *gin.Context, app *store.App, cfg *store.AppConfig, resp offlineResponse) {
	txErr := h.store.DB().Transaction(func(tx *gorm.DB) error {
		return tx.Delete(&store.AppConfig{}, cfg.ID).Error
	})
	if txErr != nil {
		h.fail(c, http.StatusInternalServerError, txErr, "")
		return
	}
	resp.ConfigDeleted = true
	var remaining int64
	if err := h.store.DB().Model(&store.AppConfig{}).Where("app_id = ?", app.ID).Count(&remaining).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	resp.AppUndeployed = remaining == 0
	c.JSON(http.StatusOK, resp)
}
