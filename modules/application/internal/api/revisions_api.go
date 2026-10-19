package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/application/internal/render"
	"github.com/funcx27/kite/modules/application/internal/store"
)

type revisionSummary struct {
	Rev    int    `json:"rev"`
	Source string `json:"source"`
	// RefRev marks the revision a derived one was built from (rollback: the
	// rev its content came from); 0 when not applicable.
	RefRev int `json:"refRev"`
	// OperatorName is the operator's resolved display name; empty when the
	// user no longer exists.
	OperatorName string    `json:"operatorName"`
	OperatorID   uint      `json:"operatorId"`
	CreatedAt    time.Time `json:"createdAt"`
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
	ids := make([]uint, 0, len(revs))
	seen := map[uint]bool{}
	for _, r := range revs {
		if r.OperatorID > 0 && !seen[r.OperatorID] {
			seen[r.OperatorID] = true
			ids = append(ids, r.OperatorID)
		}
	}
	names := h.deps.UserNames(ids)
	out := make([]revisionSummary, 0, len(revs))
	for _, r := range revs {
		out = append(out, revisionSummary{
			Rev: r.Rev, Source: r.Source, RefRev: r.RefRev,
			OperatorID: r.OperatorID, OperatorName: names[r.OperatorID],
			CreatedAt: r.CreatedAt,
			Redacted:  containsSecretDocs(store.JSONField(r.Manifests)),
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
// desired configuration (design §7). With {deploy:true} the same request
// continues through the publish, so rollback is one confirm action that both
// persists the revision and applies it; without it the manifests stay pending
// for a later publish.
func (h *Handler) RollbackConfig(c *gin.Context) {
	app, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	if !h.authorize(c, "update", cfg.Cluster, cfg.Namespace) {
		return
	}
	var req struct {
		Rev    *int `json:"rev"`
		Deploy bool `json:"deploy"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Rev == nil {
		h.fail(c, http.StatusBadRequest, nil, "rev is required")
		return
	}
	var row store.ConfigRevision
	if err := h.store.DB().Where("app_config_id = ? AND rev = ?", cfg.ID, *req.Rev).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.fail(c, http.StatusNotFound, nil, "revision not found")
			return
		}
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	var cli client.Client
	if req.Deploy {
		var err error
		cli, err = h.deps.ResolveCluster(c.Request.Context(), cfg.Cluster)
		if err != nil {
			h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+cfg.Cluster)
			return
		}
	}
	data := []byte(row.Manifests)
	// A config with a pull credential keeps referencing the managed secret
	// regardless of which revision it rolls back to: inject before the
	// persist so the revision, the stored manifests and the applied set stay
	// identical and the reference never becomes drift.
	rollbackDocs, err := store.DecodeManifests(data)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	render.InjectPullSecret(rollbackDocs, pullSecretName(cfg.CredentialRef))
	if data, err = store.EncodeManifests(rollbackDocs); err != nil {
		h.fail(c, http.StatusInternalServerError, err, "encode rollback manifests")
		return
	}
	previous := string(cfg.Manifests)
	newRev := 0
	txErr := h.store.DB().Transaction(func(tx *gorm.DB) error {
		cur, err := lockConfig(tx, cfg.ID)
		if err != nil {
			return err
		}
		if err := tx.Model(&store.AppConfig{}).Where("id = ?", cur.ID).
			Update("manifests", store.SecretJSON(data)).Error; err != nil {
			return err
		}
		newRev, err = store.AppendRevisionTx(tx, cur.ID, store.RevSourceRollback, data, h.deps.OperatorID(c), *req.Rev)
		return err
	})
	if txErr != nil {
		h.fail(c, http.StatusInternalServerError, txErr, "")
		return
	}
	h.recordAudit(h.requestIdentity(c), store.HistoryEntry{
		Cluster: cfg.Cluster, ResourceType: authzResource, ResourceName: app.Name,
		Namespace: cfg.Namespace, Operation: "update",
		YAML:         redacted(data),
		PreviousYAML: redacted(store.JSONField(previous)),
	})
	if !req.Deploy {
		c.JSON(http.StatusOK, gin.H{"latestRev": newRev, "rolledBackTo": row.Rev})
		return
	}
	// The rollback persists first (its revision records the intent), mirroring
	// the deploy path's image persist.
	cfg.Manifests = store.SecretJSON(data)
	resp, err := h.applyPublish(c.Request.Context(), h.requestIdentity(c), app, cfg, cli, rollbackDocs, false, newRev, "")
	var nf *namespaceForbiddenError
	var noChange *noChangeError
	switch {
	case errors.As(err, &nf):
		h.fail(c, http.StatusForbidden, err, err.Error())
		return
	case errors.As(err, &noChange):
		// Rolled back to content the cluster already runs: the revision
		// landed (or deduped), nothing to apply.
		c.JSON(http.StatusOK, gin.H{
			"latestRev":    newRev,
			"rolledBackTo": row.Rev,
			"success":      true,
			"results":      []deployDocResult{},
		})
		return
	case err != nil:
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"latestRev":    newRev,
		"rolledBackTo": row.Rev,
		"success":      resp.Success,
		"results":      resp.Results,
	})
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
