package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
	ReadyReplicas   *int64     `json:"readyReplicas"`
	Replicas        *int64     `json:"replicas"`
	// SpaceUnlinked marks a config whose namespace lost the space label (space
	// was unlinked on that cluster) — the config stays visible with its own
	// state instead of being filtered away. Absent when the cluster could not
	// be queried, degrading to fully visible.
	SpaceUnlinked bool `json:"spaceUnlinked,omitempty"`
}

type createAppRequest struct {
	Name        string          `json:"name" binding:"required"`
	Namespace   string          `json:"namespace"`
	Description string          `json:"description"`
	Spec        json.RawMessage `json:"spec"`
	// Cluster binds the app to a cluster at creation time and first-publishes
	// the rendered draft to it (create + config + rev1 in one transaction);
	// empty keeps the draft-only behavior.
	Cluster string `json:"cluster,omitempty"`
}

// createAppResponse is the create response when the request carries a target
// cluster: the app view plus the first publish's outcome, the deploy field
// matching the deploy endpoint's response shape.
type createAppResponse struct {
	appResponse
	Deploy *deployResponse `json:"deploy,omitempty"`
}

type updateAppRequest struct {
	Description string          `json:"description"`
	Spec        json.RawMessage `json:"spec"`
}

// toAppResponse annotates each config with SpaceUnlinked against the labeled
// spaces index; a nil or cluster-missing index annotates nothing.
func toAppResponse(app *store.App, spaces map[string]map[string]bool) appResponse {
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
		sum := configSummary{
			ID:              cfg.ID,
			Cluster:         cfg.Cluster,
			Namespace:       cfg.Namespace,
			StatusSummary:   cfg.StatusSummary,
			StatusCheckedAt: cfg.StatusCheckedAt,
			LastDeployedAt:  cfg.LastDeployedAt,
			ReadyReplicas:   cfg.ReadyReplicas,
			Replicas:        cfg.Replicas,
		}
		if labeled, ok := spaces[cfg.Cluster]; ok && !labeled[cfg.Namespace] {
			sum.SpaceUnlinked = true
		}
		resp.Configs = append(resp.Configs, sum)
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
	scoped := make([]store.App, 0, len(apps))
	for i := range apps {
		if !h.deps.Authorize(c, authzResource, "get", cluster, apps[i].Namespace) {
			continue
		}
		scoped = append(scoped, apps[i])
	}
	spaces := h.labeledSpaces(c.Request.Context(), appConfigClusters(scoped))
	visible := make([]appResponse, 0, len(scoped))
	for i := range scoped {
		visible = append(visible, toAppResponse(&scoped[i], spaces))
	}
	c.JSON(http.StatusOK, gin.H{"applications": visible, "total": len(visible)})
}

// labeledSpaces maps each queried cluster to the set of namespaces carrying
// the space marker, read with one label-selector list per cluster. A cluster
// that cannot be queried (unknown or offline) is absent from the map, which
// callers treat as "keep everything visible" — unreachable clusters degrade
// like the status column's unreachable marker, they never hide applications.
func (h *Handler) labeledSpaces(ctx context.Context, clusters []string) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(clusters))
	for _, cluster := range clusters {
		cli, err := h.deps.ResolveCluster(ctx, cluster)
		if err != nil {
			continue
		}
		list := &unstructured.UnstructuredList{}
		list.SetAPIVersion("v1")
		list.SetKind("NamespaceList")
		if err := cli.List(ctx, list, client.MatchingLabels{store.LabelKiteSpace: "true"}); err != nil {
			continue
		}
		names := make(map[string]bool, len(list.Items))
		for i := range list.Items {
			if name := list.Items[i].GetName(); name != "" {
				names[name] = true
			}
		}
		out[cluster] = names
	}
	return out
}

// appConfigClusters returns the distinct clusters referenced by the configs.
func appConfigClusters(apps []store.App) []string {
	seen := map[string]bool{}
	for i := range apps {
		for j := range apps[i].Configs {
			seen[apps[i].Configs[j].Cluster] = true
		}
	}
	clusters := make([]string, 0, len(seen))
	for name := range seen {
		clusters = append(clusters, name)
	}
	return clusters
}

// validateCreateAppReq parses the create body and enforces the DNS-1123 and
// JSON rules; false means the response is already written.
func (h *Handler) validateCreateAppReq(c *gin.Context, req *createAppRequest) bool {
	if err := c.ShouldBindJSON(req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return false
	}
	if !render.ValidateDNS1123Label(req.Name, 63) {
		h.fail(c, http.StatusBadRequest, nil, "name must be a DNS-1123 label of at most 63 characters")
		return false
	}
	if req.Namespace == "" {
		h.fail(c, http.StatusBadRequest, nil, "namespace is required")
		return false
	}
	if !render.ValidateDNS1123Label(req.Namespace, 63) {
		h.fail(c, http.StatusBadRequest, nil, "namespace must be a DNS-1123 label")
		return false
	}
	if req.Spec != nil && !json.Valid(req.Spec) {
		h.fail(c, http.StatusBadRequest, nil, "spec must be valid JSON")
		return false
	}
	return true
}

func (h *Handler) CreateApp(c *gin.Context) {
	var req createAppRequest
	if !h.validateCreateAppReq(c, &req) {
		return
	}
	// With a target cluster nothing may be written unless that cluster is
	// reachable and the create verb passes there — the request also publishes.
	var cli client.Client
	authCluster := c.GetString(clusterNameContextKey)
	if req.Cluster != "" {
		authCluster = req.Cluster
		var err error
		if cli, err = h.deps.ResolveCluster(c.Request.Context(), req.Cluster); err != nil {
			h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+req.Cluster)
			return
		}
	}
	if !h.authorize(c, "create", authCluster, req.Namespace) {
		return
	}
	db := h.store.DB()
	var count int64
	if err := db.Model(&store.App{}).Where("namespace = ? AND name = ?", req.Namespace, req.Name).Count(&count).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if count > 0 {
		h.fail(c, http.StatusConflict, nil, "application already exists in this space")
		return
	}
	app := store.App{Name: req.Name, Namespace: req.Namespace, Description: req.Description, Spec: store.JSONField(req.Spec)}
	id := h.requestIdentity(c)
	if req.Cluster == "" {
		if err := db.Create(&app).Error; err != nil {
			h.fail(c, http.StatusInternalServerError, err, "")
			return
		}
		h.recordAudit(id, store.HistoryEntry{
			Cluster: authCluster, ResourceType: authzResource, ResourceName: app.Name,
			Namespace: app.Namespace, Operation: "create", YAML: redacted(app.Spec),
		})
		c.JSON(http.StatusCreated, toAppResponse(&app, nil))
		return
	}
	h.createAndDeployApp(c, id, &app, req.Cluster, cli)
}

// createAndDeployApp runs the create-with-first-publish chain: the rendered
// draft spec becomes the app row, its config and rev1 in one transaction,
// then the regular publish pipeline pushes the manifest set. Per-document
// failures do not fail the request — the app exists and the config keeps its
// error state; the deploy outcome rides the response's deploy field.
func (h *Handler) createAndDeployApp(c *gin.Context, id identity, app *store.App, cluster string, cli client.Client) {
	docs, err := renderAppSpec(app)
	if err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	manifests, err := store.EncodeManifests(docs)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	var cfg store.AppConfig
	txErr := h.store.DB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(app).Error; err != nil {
			return err
		}
		var err error
		cfg, err = createConfigTx(tx, app, cluster, manifests, store.RevSourceRender, "", id.operatorID)
		return err
	})
	if isConcurrentWriteError(txErr) {
		h.fail(c, http.StatusConflict, nil, "application already exists in this space")
		return
	}
	if txErr != nil {
		h.fail(c, http.StatusInternalServerError, txErr, "")
		return
	}
	h.auditAppCreated(id, app, &cfg, manifests)
	// createConfigTx wrote rev1 in the same transaction; this publish is
	// exactly that revision.
	resp, err := h.applyPublish(c.Request.Context(), id, app, &cfg, cli, docs, false, 1, "")
	if !h.publishFailure(c, err) {
		return
	}
	c.JSON(http.StatusCreated, createAppResponse{appResponse: toAppResponse(app, nil), Deploy: &resp})
}

// auditAppCreated records the app-create and config-create history entries of
// the create-with-first-publish chain.
func (h *Handler) auditAppCreated(id identity, app *store.App, cfg *store.AppConfig, manifests []byte) {
	h.recordAudit(id, store.HistoryEntry{
		Cluster: cfg.Cluster, ResourceType: authzResource, ResourceName: app.Name,
		Namespace: app.Namespace, Operation: "create", YAML: redacted(app.Spec),
	})
	h.recordAudit(id, store.HistoryEntry{
		Cluster: cfg.Cluster, ResourceType: authzResource, ResourceName: app.Name,
		Namespace: cfg.Namespace, Operation: "create", YAML: redacted(manifests),
	})
}

func (h *Handler) GetApp(c *gin.Context) {
	app := h.findAppOr404(c)
	if app == nil {
		return
	}
	if !h.authorize(c, "get", c.GetString(clusterNameContextKey), app.Namespace) {
		return
	}
	spaces := h.labeledSpaces(c.Request.Context(), appConfigClusters([]store.App{*app}))
	c.JSON(http.StatusOK, toAppResponse(app, spaces))
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
	c.JSON(http.StatusOK, toAppResponse(app, nil))
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
