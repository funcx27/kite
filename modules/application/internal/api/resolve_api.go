package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/funcx27/kite/modules/application/internal/store"
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

// resolveAdoption is one document's selected drift paths: the live subtree at
// each path is adopted into the stored manifests.
type resolveAdoption struct {
	Kind      string   `json:"kind"`
	Namespace string   `json:"namespace,omitempty"`
	Name      string   `json:"name"`
	Paths     []string `json:"paths"`
}

type resolveRequest struct {
	// ExpectedRev guards the persist against concurrent saves (same contract
	// as UpdateConfig).
	ExpectedRev *int `json:"expectedRev"`
	// PreviewToken pins the diff the caller decided on; a stale token 409s.
	PreviewToken string            `json:"previewToken"`
	Adoptions    []resolveAdoption `json:"adoptions"`
}

// ResolveConfig adopts cluster truth into the stored manifests (选边): for
// every adopted path the live subtree — the real values, not the redacted
// status view — replaces the manifests value, and the anchor is refreshed to
// the current live snapshot; an empty adoption list still re-anchors, since
// confirming is itself the decision. One Source=resolve revision records the
// change. The response is the config's fresh status.
func (h *Handler) ResolveConfig(c *gin.Context) {
	app, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	if !h.authorize(c, "update", cfg.Cluster, cfg.Namespace) {
		return
	}
	var req resolveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	if req.ExpectedRev == nil {
		h.fail(c, http.StatusBadRequest, nil, "expectedRev is required")
		return
	}
	if req.PreviewToken == "" {
		h.fail(c, http.StatusBadRequest, nil, "previewToken is required")
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
		h.fail(c, http.StatusBadRequest, nil, "config has no manifests to resolve")
		return
	}
	live, err := syncops.FetchLiveDocs(c.Request.Context(), cli, syncops.IdentityTemplates(manifests, nil))
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}

	// The token must still describe the current manifests/live diff.
	if diff := syncops.DiffManifests(manifests, syncops.LiveList(live, manifests)); previewTokenHash(diff) != req.PreviewToken {
		h.failStalePreview(c)
		return
	}

	manIdx := docMap(manifests)
	for _, ad := range req.Adoptions {
		key := store.DocKey(ad.Kind, ad.Namespace, ad.Name)
		md := manIdx[key]
		if md == nil {
			h.fail(c, http.StatusBadRequest, nil, "manifests have no document "+key)
			return
		}
		ld := live[key]
		if ld == nil {
			h.fail(c, http.StatusBadRequest, nil, "cluster has no document "+key)
			return
		}
		// Paths refer to the normalized views the status endpoint serves.
		liveDoc := syncops.Normalize(ld)
		for _, p := range ad.Paths {
			set := interface{}(syncops.DeleteValue)
			if val, found := syncops.PathValue(liveDoc.Object, p); found {
				set = val
			}
			if err := syncops.SetPathValue(md.Object, p, set); err != nil {
				h.fail(c, http.StatusBadRequest, nil, err.Error())
				return
			}
		}
	}

	data, err := store.EncodeManifests(manifests)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	// The anchor takes the current live snapshot (normalized, the same shape
	// readBack leaves behind): resolve re-anchors by definition.
	anchorDocs := syncops.NormalizeManifests(syncops.LiveList(live, manifests))
	anchorData, err := store.EncodeManifests(anchorDocs)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}

	previous := string(cfg.Manifests)
	now := time.Now()
	post := statusOf(manifests, anchorDocs, live)
	txErr := h.store.DB().Transaction(func(tx *gorm.DB) error {
		cur, err := lockConfig(tx, cfg.ID)
		if err != nil {
			return err
		}
		var maxRev int
		if err := tx.Model(&store.ConfigRevision{}).
			Where("app_config_id = ?", cur.ID).
			Select("COALESCE(MAX(rev), 0)").
			Scan(&maxRev).Error; err != nil {
			return err
		}
		if *req.ExpectedRev != maxRev {
			return ErrConfigModified
		}
		// The revision lands first so applied_rev can name it: resolving
		// re-anchors by definition, so the resolve revision is what the
		// cluster now runs.
		newRev, err := store.AppendRevisionTx(tx, cur.ID, store.RevSourceResolve, data, h.deps.OperatorID(c), 0)
		if err != nil {
			return err
		}
		return tx.Model(&store.AppConfig{}).Where("id = ?", cur.ID).Updates(map[string]interface{}{
			"manifests":         store.SecretJSON(data),
			"last_applied":      store.SecretJSON(anchorData),
			"applied_rev":       newRev,
			"status_summary":    post.Status,
			"status_checked_at": now,
		}).Error
	})
	switch {
	case errors.Is(txErr, ErrConfigModified), isConcurrentWriteError(txErr):
		h.fail(c, http.StatusConflict, nil, ErrConfigModified.Error())
		return
	case txErr != nil:
		h.fail(c, http.StatusInternalServerError, txErr, "")
		return
	}
	h.recordAudit(h.requestIdentity(c), store.HistoryEntry{
		Cluster: cfg.Cluster, ResourceType: authzResource, ResourceName: app.Name,
		Namespace: cfg.Namespace, Operation: "update",
		YAML:         redacted(data),
		PreviousYAML: redacted([]byte(previous)),
	})
	post.CheckedAt = now
	c.JSON(http.StatusOK, post)
}
