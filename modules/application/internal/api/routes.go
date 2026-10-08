package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/application/internal/store"
)

// clusterNameContextKey mirrors the host ClusterMiddleware's context key; the
// module cannot import the host package, so the value is repeated here.
const clusterNameContextKey = "cluster-name"

// authzResource is the pseudo-resource all module operations authorize
// against (design §7: 定义 CRUD 走 Authorize(c,"applications",verb,cluster,ns)).
const authzResource = "applications"

// credentialsResource is the credential center pseudo-resource: referencing a
// credential consumes its secret material, which requires get on the center
// (usage authorization, judged host-wide like the center's own CRUD) — the
// same gate the workflow build node and the REST build path apply.
const credentialsResource = "credentials"

// Deps is the set of host capabilities the HTTP handlers depend on.
type Deps interface {
	// ResolveCluster returns the controller-runtime client of the named
	// cluster, or an error if the cluster is unknown or offline.
	ResolveCluster(ctx context.Context, name string) (client.Client, error)
	// ClusterNames lists the names of the clusters the host currently knows
	// about; cross-cluster views (space quotas) iterate them.
	ClusterNames() []string
	// Authorize reports whether the request may perform verb on resource in
	// cluster/namespace, judged against the host RBAC roles carried in the
	// gin context.
	Authorize(c *gin.Context, resource, verb, cluster, namespace string) bool
	// AuthorizeUser reports whether the given user may perform verb on
	// resource in cluster/namespace. The exported deploy facade runs outside
	// any request (workflow runs execute with their trigger user's identity),
	// so its checks go by user id; unknown users are denied.
	AuthorizeUser(userID uint, resource, verb, cluster, namespace string) bool
	// OperatorID returns the ID of the authenticated user of the request.
	OperatorID(c *gin.Context) uint
	// UserNames resolves user IDs to display names for history views; deleted
	// or unknown IDs are simply absent from the map.
	UserNames(ids []uint) map[uint]string
	// RecordHistory persists an audit entry for a state change.
	RecordHistory(entry store.HistoryEntry)
	// PullSecretData resolves a credentialRef to its registry auth table
	// (host -> user/pass) for the managed image pull secret; git entries are
	// ignored. An unknown credentialRef is an error.
	PullSecretData(credentialRef string) (map[string]store.RegistryAuth, error)
}

// Handler serves the application module HTTP API.
type Handler struct {
	deps  Deps
	store *store.Store
}

// RegisterRoutes mounts the module API routes under /applications.
func RegisterRoutes(api *gin.RouterGroup, deps Deps, st *store.Store) {
	h := &Handler{deps: deps, store: st}
	g := api.Group("/applications")
	g.GET("", h.ListApps)
	g.POST("", h.CreateApp)
	g.POST("/preview", h.PreviewRender)
	g.GET("/:name", h.GetApp)
	g.PUT("/:name", h.UpdateApp)
	g.DELETE("/:name", h.DeleteApp)
	g.PUT("/spaces/:name/quotas", h.UpsertSpaceQuotas)
	g.GET("/spaces/:name/quotas", h.GetSpaceQuotas)
	// Dissociating removes only the space marker; the namespace itself is
	// never deleted here.
	g.DELETE("/spaces/:name", h.DissociateSpace)
	// The config-cluster param is deliberately not named "cluster": the host
	// ClusterMiddleware resolves any :cluster param and 404s when that cluster
	// is gone from the platform, but a config slot may outlive its cluster —
	// offline must still reach the handler to retire it.
	g.GET("/:name/configs/:cfgCluster", h.GetConfig)
	g.PUT("/:name/configs/:cfgCluster", h.UpdateConfig)
	g.POST("/:name/configs/:cfgCluster/deploy", h.DeployConfig)
	g.POST("/:name/configs/:cfgCluster/sync", h.SyncConfig)
	g.POST("/:name/configs/:cfgCluster/offline", h.OfflineConfig)
	g.GET("/:name/configs/:cfgCluster/merge-preview", h.MergePreview)
	g.POST("/:name/configs/:cfgCluster/merge", h.MergeConfig)
	g.GET("/:name/configs/:cfgCluster/status", h.GetStatus)
	g.GET("/:name/configs/:cfgCluster/resources", h.GetResources)
	g.POST("/:name/configs/:cfgCluster/restart", h.RestartConfig)
	g.POST("/:name/configs/:cfgCluster/pods/:pod/restart", h.RestartPod)
	g.POST("/:name/configs/:cfgCluster/pods/:pod/force-delete", h.ForceDeletePod)
	g.GET("/:name/configs/:cfgCluster/revisions", h.ListRevisions)
	g.GET("/:name/configs/:cfgCluster/revisions/:rev", h.GetRevision)
	g.POST("/:name/configs/:cfgCluster/rollback", h.RollbackConfig)
}

// authorize runs the module-side permission check and writes the 403 response
// when access is denied.
func (h *Handler) authorize(c *gin.Context, verb, cluster, namespace string) bool {
	if h.deps.Authorize(c, authzResource, verb, cluster, namespace) {
		return true
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "user does not have permission to " + verb + " " + authzResource + " in namespace " + namespace + " on cluster " + cluster})
	return false
}

func (h *Handler) fail(c *gin.Context, status int, err error, message string) {
	if err != nil {
		message = err.Error()
	}
	c.JSON(status, gin.H{"error": message})
}

// findApp loads the app by name with its configs preloaded.
func (h *Handler) findApp(name string) (*store.App, error) {
	var app store.App
	if err := h.store.DB().Preload("Configs").Where("name = ?", name).First(&app).Error; err != nil {
		return nil, err
	}
	return &app, nil
}

func (h *Handler) findAppOr404(c *gin.Context) *store.App {
	app, err := h.findApp(c.Param("name"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		h.fail(c, http.StatusNotFound, nil, "application not found")
		return nil
	}
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return nil
	}
	return app
}

// findConfig loads the per-cluster config of the app.
func (h *Handler) findConfig(appID uint, cluster string) (*store.AppConfig, error) {
	var cfg store.AppConfig
	err := h.store.DB().Where("app_id = ? AND cluster = ?", appID, cluster).First(&cfg).Error
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

// findAppConfig loads the app and its per-cluster config for the action
// endpoints, writing the 404 response when either is missing.
func (h *Handler) findAppConfig(c *gin.Context) (*store.App, *store.AppConfig, bool) {
	app := h.findAppOr404(c)
	if app == nil {
		return nil, nil, false
	}
	cfg, err := h.findConfig(app.ID, c.Param("cfgCluster"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		h.fail(c, http.StatusNotFound, nil, "config not found for cluster "+c.Param("cfgCluster"))
		return nil, nil, false
	}
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return nil, nil, false
	}
	return app, cfg, true
}

// latestRev returns the newest revision number of the config's chain; a chain
// with no revisions yet is at rev 0 (the optimistic-lock baseline).
func (h *Handler) latestRev(appConfigID uint) (int, error) {
	var rev int
	err := h.store.DB().Model(&store.ConfigRevision{}).
		Where("app_config_id = ?", appConfigID).
		Select("COALESCE(MAX(rev), 0)").
		Scan(&rev).Error
	return rev, err
}

// recordAudit persists an audit entry attributed to the acting identity.
func (h *Handler) recordAudit(id identity, entry store.HistoryEntry) {
	entry.OperatorID = id.operatorID
	entry.Success = true
	h.deps.RecordHistory(entry)
}

// recordOperation persists an audit entry with an explicit outcome: per-doc
// deploy/sync/offline operations also record their failures (design §6).
func (h *Handler) recordOperation(id identity, entry store.HistoryEntry, success bool, errMsg string) {
	entry.OperatorID = id.operatorID
	entry.Success = success
	entry.Err = errMsg
	h.deps.RecordHistory(entry)
}

// bindOptionalJSON parses an optional JSON body; an empty body yields the
// zero struct so endpoints like deploy/offline/merge accept bare POSTs.
func bindOptionalJSON(c *gin.Context, req interface{}) error {
	if c.Request.Body == nil || c.Request.ContentLength == 0 {
		return nil
	}
	return c.ShouldBindJSON(req)
}
