package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/funcx27/kite/modules/application/internal/drift"
	"github.com/funcx27/kite/modules/application/internal/store"
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

type statusResponse struct {
	Status    string            `json:"status"`
	Action    string            `json:"action,omitempty"`
	CheckedAt time.Time         `json:"checkedAt"`
	Drift     []syncops.DocDiff `json:"drift"`
	Pending   []syncops.DocDiff `json:"pending"`
	Error     string            `json:"error,omitempty"`
}

// GetStatus computes the on-demand drift report (design §7): the two diff
// groups against the anchor plus the suggested action, and writes the
// outcome back to StatusSummary/StatusCheckedAt so the list page aggregates
// the persisted values without re-diffing (design §3/§11). A failed
// detection is itself a check outcome: the config is marked error and the
// message is returned in the body. Undeployed configs (empty anchor) answer
// from the DB alone, so a down cluster never blocks their report.
func (h *Handler) GetStatus(c *gin.Context) {
	_, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	if !h.authorize(c, "get", cfg.Cluster, cfg.Namespace) {
		return
	}
	now := time.Now()

	rep, checkErr := h.computeStatus(c, cfg)
	if checkErr != nil {
		rep = drift.Result{Status: store.StatusError, Drift: []syncops.DocDiff{}, Pending: []syncops.DocDiff{}}
	}
	if err := h.store.DB().Model(&store.AppConfig{}).Where("id = ?", cfg.ID).Updates(map[string]interface{}{
		"status_summary":    rep.Status,
		"status_checked_at": now,
	}).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	resp := statusResponse{
		Status:    rep.Status,
		Action:    rep.Action,
		CheckedAt: now,
		Drift:     rep.Drift,
		Pending:   rep.Pending,
	}
	if checkErr != nil {
		resp.Error = checkErr.Error()
	}
	c.JSON(http.StatusOK, resp)
}

// computeStatus runs the drift check for one config: the live objects are
// only fetched when an anchor exists to compare them against. The knowing
// override whitelist feeds the merged-state detection, so a confirmed merge
// is never re-reported as conflict (design §3/§4).
func (h *Handler) computeStatus(c *gin.Context, cfg *store.AppConfig) (drift.Result, error) {
	manifests, err := store.DecodeManifests(cfg.Manifests)
	if err != nil {
		return drift.Result{}, fmt.Errorf("decode manifests: %w", err)
	}
	anchor, err := store.DecodeManifests(cfg.LastApplied)
	if err != nil {
		return drift.Result{}, fmt.Errorf("decode anchor: %w", err)
	}
	if len(anchor) == 0 {
		return drift.Check(manifests, anchor, nil, nil), nil
	}
	cli, err := h.deps.ResolveCluster(c.Request.Context(), cfg.Cluster)
	if err != nil {
		return drift.Result{}, fmt.Errorf("resolve cluster %s: %w", cfg.Cluster, err)
	}
	templates := syncops.IdentityTemplates(manifests, anchor)
	live, err := syncops.FetchLiveDocs(c.Request.Context(), cli, templates)
	if err != nil {
		return drift.Result{}, fmt.Errorf("fetch live objects: %w", err)
	}
	return drift.Check(manifests, anchor, live, decodeScopedPaths(cfg.OverriddenPaths)), nil
}
