package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/funcx27/kite/modules/application/internal/render"
	"github.com/funcx27/kite/modules/application/internal/store"
)

// appResponse is the API view of an app; JSONField values are re-typed as
// json.RawMessage so they serialize as JSON instead of base64 bytes.
type appResponse struct {
	ID          uint            `json:"id"`
	Name        string          `json:"name"`
	Namespace   string          `json:"namespace"`
	Description string          `json:"description"`
	Spec        json.RawMessage `json:"spec"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	Configs     []configSummary `json:"configs"`
}

// configSummary is the per-cluster line shown on the app list/detail; the
// aggregated status comes from the persisted StatusSummary (design §11).
type configSummary struct {
	ID              uint       `json:"id"`
	Cluster         string     `json:"cluster"`
	Namespace       string     `json:"namespace"`
	StatusSummary   string     `json:"statusSummary"`
	StatusCheckedAt *time.Time `json:"statusCheckedAt"`
	LastDeployedAt  *time.Time `json:"lastDeployedAt"`
}

type createAppRequest struct {
	Name        string          `json:"name" binding:"required"`
	Namespace   string          `json:"namespace"`
	Description string          `json:"description"`
	Spec        json.RawMessage `json:"spec"`
}

type updateAppRequest struct {
	Description string          `json:"description"`
	Spec        json.RawMessage `json:"spec"`
}

func toAppResponse(app *store.App) appResponse {
	resp := appResponse{
		ID:          app.ID,
		Name:        app.Name,
		Namespace:   app.Namespace,
		Description: app.Description,
		Spec:        json.RawMessage(app.Spec),
		CreatedAt:   app.CreatedAt,
		UpdatedAt:   app.UpdatedAt,
		Configs:     make([]configSummary, 0, len(app.Configs)),
	}
	for _, cfg := range app.Configs {
		resp.Configs = append(resp.Configs, configSummary{
			ID:              cfg.ID,
			Cluster:         cfg.Cluster,
			Namespace:       cfg.Namespace,
			StatusSummary:   cfg.StatusSummary,
			StatusCheckedAt: cfg.StatusCheckedAt,
			LastDeployedAt:  cfg.LastDeployedAt,
		})
	}
	return resp
}

// ListApps returns the apps the caller can access in the request's current
// cluster context, filtered per app by namespace.
func (h *Handler) ListApps(c *gin.Context) {
	var apps []store.App
	if err := h.store.DB().Preload("Configs").Find(&apps).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	cluster := c.GetString(clusterNameContextKey)
	visible := make([]appResponse, 0, len(apps))
	for i := range apps {
		if !h.deps.Authorize(c, authzResource, "get", cluster, apps[i].Namespace) {
			continue
		}
		visible = append(visible, toAppResponse(&apps[i]))
	}
	c.JSON(http.StatusOK, gin.H{"applications": visible, "total": len(visible)})
}

func (h *Handler) CreateApp(c *gin.Context) {
	var req createAppRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	if !render.ValidateDNS1123Label(req.Name, 63) {
		h.fail(c, http.StatusBadRequest, nil, "name must be a DNS-1123 label of at most 63 characters")
		return
	}
	if req.Namespace == "" {
		req.Namespace = "default"
	}
	if !render.ValidateDNS1123Label(req.Namespace, 63) {
		h.fail(c, http.StatusBadRequest, nil, "namespace must be a DNS-1123 label")
		return
	}
	if req.Spec != nil && !json.Valid(req.Spec) {
		h.fail(c, http.StatusBadRequest, nil, "spec must be valid JSON")
		return
	}
	cluster := c.GetString(clusterNameContextKey)
	if !h.authorize(c, "create", cluster, req.Namespace) {
		return
	}
	db := h.store.DB()
	var count int64
	if err := db.Model(&store.App{}).Where("name = ?", req.Name).Count(&count).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if count > 0 {
		h.fail(c, http.StatusConflict, nil, "application already exists")
		return
	}
	app := store.App{Name: req.Name, Namespace: req.Namespace, Description: req.Description, Spec: store.JSONField(req.Spec)}
	if err := db.Create(&app).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	h.recordAudit(h.requestIdentity(c), store.HistoryEntry{
		Cluster: cluster, ResourceType: authzResource, ResourceName: app.Name,
		Namespace: app.Namespace, Operation: "create", YAML: redacted(app.Spec),
	})
	c.JSON(http.StatusCreated, toAppResponse(&app))
}

func (h *Handler) GetApp(c *gin.Context) {
	app := h.findAppOr404(c)
	if app == nil {
		return
	}
	if !h.authorize(c, "get", c.GetString(clusterNameContextKey), app.Namespace) {
		return
	}
	c.JSON(http.StatusOK, toAppResponse(app))
}

func (h *Handler) UpdateApp(c *gin.Context) {
	app := h.findAppOr404(c)
	if app == nil {
		return
	}
	if !h.authorize(c, "update", c.GetString(clusterNameContextKey), app.Namespace) {
		return
	}
	var req updateAppRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	if req.Spec != nil && !json.Valid(req.Spec) {
		h.fail(c, http.StatusBadRequest, nil, "spec must be valid JSON")
		return
	}
	previous := app.Spec
	app.Description = req.Description
	if req.Spec != nil {
		app.Spec = store.JSONField(req.Spec)
	}
	if err := h.store.DB().Save(app).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	h.recordAudit(h.requestIdentity(c), store.HistoryEntry{
		Cluster: c.GetString(clusterNameContextKey), ResourceType: authzResource, ResourceName: app.Name,
		Namespace: app.Namespace, Operation: "update",
		YAML:         redacted(app.Spec),
		PreviousYAML: redacted(previous),
	})
	c.JSON(http.StatusOK, toAppResponse(app))
}

// DeleteApp removes an app only when none of its cluster configs is deployed
// (anchor present); offline configs and revision chains are cascaded away.
func (h *Handler) DeleteApp(c *gin.Context) {
	app := h.findAppOr404(c)
	if app == nil {
		return
	}
	if !h.authorize(c, "delete", c.GetString(clusterNameContextKey), app.Namespace) {
		return
	}
	deployed := make([]string, 0, len(app.Configs))
	for _, cfg := range app.Configs {
		if len(cfg.LastApplied) > 0 {
			deployed = append(deployed, cfg.Cluster)
		}
	}
	if len(deployed) > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error":    "application has deployed configs, offline them first",
			"clusters": deployed,
		})
		return
	}
	if err := h.store.DB().Delete(app).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	h.recordAudit(h.requestIdentity(c), store.HistoryEntry{
		Cluster: c.GetString(clusterNameContextKey), ResourceType: authzResource, ResourceName: app.Name,
		Namespace: app.Namespace, Operation: "delete", YAML: redacted(app.Spec),
	})
	c.JSON(http.StatusOK, gin.H{"message": "application deleted"})
}
