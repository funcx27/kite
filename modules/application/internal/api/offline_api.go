package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/funcx27/kite/modules/application/internal/store"
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

type offlineRequest struct {
	DeleteConfig bool `json:"deleteConfig"`
}

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
// ExtraYAML, no label sweep). deleteConfig=false (default) keeps the config
// and its revision chain with a cleared anchor; deleteConfig=true removes the
// config row and cascades its revisions — only after every cluster deletion
// succeeded, since the operation is irreversible.
func (h *Handler) OfflineConfig(c *gin.Context) {
	app, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	var req offlineRequest
	if err := bindOptionalJSON(c, &req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
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
		h.finalizeOffline(c, app, cfg, req, resp)
		return
	}
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
	if !resp.Success {
		// Never delete the config (irreversible) while cluster objects may
		// remain; surface the partial failure instead.
		if req.DeleteConfig {
			h.fail(c, http.StatusConflict, nil, "cluster deletion incomplete, config kept for retry")
			return
		}
		if err := h.updateConfigStatus(cfg, store.StatusError); err != nil {
			h.fail(c, http.StatusInternalServerError, err, "")
			return
		}
		c.JSON(http.StatusOK, resp)
		return
	}

	h.finalizeOffline(c, app, cfg, req, resp)
}

// finalizeOffline runs the config bookkeeping after the cluster side is
// settled: purge the config row (only reachable when every deletion succeeded
// or the cluster is gone) or mark the kept config undeployed, then write resp.
func (h *Handler) finalizeOffline(c *gin.Context, app *store.App, cfg *store.AppConfig, req offlineRequest, resp offlineResponse) {
	if req.DeleteConfig {
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
	} else {
		if err := h.offlineKeepConfig(cfg); err != nil {
			h.fail(c, http.StatusInternalServerError, err, "")
			return
		}
	}
	c.JSON(http.StatusOK, resp)
}

// offlineKeepConfig marks an offline config: the anchor and override
// whitelist are cleared, the manifests and revision chain stay (design §6).
func (h *Handler) offlineKeepConfig(cfg *store.AppConfig) error {
	now := time.Now()
	return h.store.DB().Transaction(func(tx *gorm.DB) error {
		return tx.Model(&store.AppConfig{}).Where("id = ?", cfg.ID).Updates(map[string]interface{}{
			"last_applied":      store.SecretJSON(nil),
			"overridden_paths":  store.JSONField(nil),
			"status_summary":    store.StatusUndeployed,
			"status_checked_at": now,
		}).Error
	})
}

// updateConfigStatus writes only the drift-check bookkeeping fields.
func (h *Handler) updateConfigStatus(cfg *store.AppConfig, summary string) error {
	return h.store.DB().Model(&store.AppConfig{}).Where("id = ?", cfg.ID).Updates(map[string]interface{}{
		"status_summary":    summary,
		"status_checked_at": time.Now(),
	}).Error
}
