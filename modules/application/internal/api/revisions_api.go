package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/funcx27/kite/modules/application/internal/store"
)

type revisionSummary struct {
	Rev        int       `json:"rev"`
	Source     string    `json:"source"`
	OperatorID uint      `json:"operatorId"`
	CreatedAt  time.Time `json:"createdAt"`
	// Redacted marks revisions that contain Secret values; the single-version
	// endpoint masks them to "***" (design §7).
	Redacted bool `json:"redacted"`
}

// ListRevisions returns the config's revision chain, newest first
// (design §7). Rollback targets stay stable: pruned rev numbers are never
// reused.
func (h *Handler) ListRevisions(c *gin.Context) {
	_, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	if !h.authorize(c, "get", cfg.Cluster, cfg.Namespace) {
		return
	}
	var revs []store.ConfigRevision
	if err := h.store.DB().Where("app_config_id = ?", cfg.ID).Order("rev DESC").Find(&revs).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	out := make([]revisionSummary, 0, len(revs))
	for _, r := range revs {
		out = append(out, revisionSummary{
			Rev: r.Rev, Source: r.Source, OperatorID: r.OperatorID,
			CreatedAt: r.CreatedAt, Redacted: containsSecretDocs(store.JSONField(r.Manifests)),
		})
	}
	c.JSON(http.StatusOK, gin.H{"revisions": out, "total": len(out)})
}

// GetRevision returns one revision's full manifest set with Secret
// data/stringData values masked (design §7).
func (h *Handler) GetRevision(c *gin.Context) {
	_, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	if !h.authorize(c, "get", cfg.Cluster, cfg.Namespace) {
		return
	}
	rev, err := strconv.Atoi(c.Param("rev"))
	if err != nil || rev < 1 {
		h.fail(c, http.StatusBadRequest, nil, "rev must be a positive integer")
		return
	}
	var row store.ConfigRevision
	if err := h.store.DB().Where("app_config_id = ? AND rev = ?", cfg.ID, rev).First(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.fail(c, http.StatusNotFound, nil, "revision not found")
			return
		}
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"rev":        row.Rev,
		"source":     row.Source,
		"operatorId": row.OperatorID,
		"createdAt":  row.CreatedAt,
		"manifests":  json.RawMessage(redacted(store.JSONField(row.Manifests))),
	})
}

// RollbackConfig writes an earlier revision's manifests back as the current
// desired configuration (design §7): a new Source=rollback revision marks the
// pending state; the cluster is only touched through the subsequent
// conflict-gated publish.
func (h *Handler) RollbackConfig(c *gin.Context) {
	app, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	if !h.authorize(c, "update", cfg.Cluster, cfg.Namespace) {
		return
	}
	var req struct {
		Rev *int `json:"rev"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Rev == nil {
		h.fail(c, http.StatusBadRequest, nil, "rev is required")
		return
	}
	var row store.ConfigRevision
	if err := h.store.DB().Where("app_config_id = ? AND rev = ?", cfg.ID, *req.Rev).First(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.fail(c, http.StatusNotFound, nil, "revision not found")
			return
		}
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	data := []byte(row.Manifests)
	previous := string(cfg.Manifests)
	newRev := 0
	txErr := h.store.DB().Transaction(func(tx *gorm.DB) error {
		cur, err := lockConfig(tx, cfg.ID)
		if err != nil {
			return err
		}
		if err := tx.Model(&store.AppConfig{}).Where("id = ?", cur.ID).Updates(map[string]interface{}{
			"manifests":        store.SecretJSON(data),
			"overridden_paths": store.JSONField(nil),
		}).Error; err != nil {
			return err
		}
		newRev, err = store.AppendRevisionTx(tx, cur.ID, store.RevSourceRollback, data, h.deps.OperatorID(c))
		return err
	})
	if txErr != nil {
		h.fail(c, http.StatusInternalServerError, txErr, "")
		return
	}
	h.recordAudit(c, store.HistoryEntry{
		Cluster: cfg.Cluster, ResourceType: authzResource, ResourceName: app.Name,
		Namespace: cfg.Namespace, Operation: "update",
		YAML:         redacted(data),
		PreviousYAML: redacted(store.JSONField(previous)),
	})
	c.JSON(http.StatusOK, gin.H{"latestRev": newRev, "rolledBackTo": row.Rev})
}

// containsSecretDocs reports whether the manifest set holds any Secret
// document; undecodable data is treated as sensitive (fail closed).
func containsSecretDocs(data store.JSONField) bool {
	docs, err := store.DecodeManifests(data)
	if err != nil {
		return true
	}
	for i := range docs {
		if docs[i].GetKind() == "Secret" {
			return true
		}
	}
	return false
}
