// Package api serves the workflow module HTTP API (design §10 M1 subset):
// definition CRUD with an optimistic-lock revision chain, runs and the
// run-lifecycle actions. Routes mount before the host RBAC middleware and
// authorize themselves against the "workflows" pseudo-resource.
package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/funcx27/kite/modules/workflow/internal/engine"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// clusterNameContextKey mirrors the host ClusterMiddleware's context key; the
// module cannot import the host package, so the value is repeated here.
const clusterNameContextKey = "cluster-name"

// authzResource is the pseudo-resource all module operations authorize
// against (design §8: CRUD → get/create/update/delete; run/cancel/retry/
// approve → exec).
const authzResource = "workflows"

// Deps is the set of host capabilities the HTTP handlers depend on.
type Deps interface {
	// Authorize reports whether the request may perform verb on resource in
	// cluster/namespace, judged against the host RBAC roles carried in the
	// gin context.
	Authorize(c *gin.Context, resource, verb, cluster, namespace string) bool
	// OperatorID returns the ID of the authenticated user of the request.
	OperatorID(c *gin.Context) uint
	// UserNames resolves user IDs to display names; deleted or unknown IDs
	// are simply absent from the map.
	UserNames(ids []uint) map[uint]string
	// IsAdmin reports whether the request's user holds the host admin role;
	// approval nodes accept any assignee or an admin (design §8).
	IsAdmin(c *gin.Context) bool
	// RecordHistory persists an audit entry for a state change.
	RecordHistory(entry store.HistoryEntry)
	// ProviderNames lists the host's available build provider names for the
	// build-settings API; empty when the build capability is not wired.
	ProviderNames() []string
	// ParseDockerfile syntax-checks inline dockerfile content with the
	// host's dockerfile frontend parser; backs save-time build-node
	// validation (a parse error is a deterministic build failure).
	ParseDockerfile(content string) error
	// ResolveGitCommit resolves a git ref to its commit SHA via one
	// ls-remote round trip (with credentials, timeout-bounded); backs
	// save-time build-node validation, which classifies the returned
	// error (deterministic vs environmental) via *exec.GitCheckError.
	ResolveGitCommit(ctx context.Context, url, ref, gitCredentialRef string) (string, error)
}

// Handler serves the workflow module HTTP API.
type Handler struct {
	deps   Deps
	store  *store.Store
	engine *engine.Engine
}

// RegisterRoutes mounts the module API: /workflows for definitions (plus
// revisions and the webhook token) and /runs for executions.
func RegisterRoutes(api *gin.RouterGroup, deps Deps, st *store.Store, eng *engine.Engine) {
	h := &Handler{deps: deps, store: st, engine: eng}
	g := api.Group("/workflows")
	g.GET("", h.ListWorkflows)
	g.POST("", h.CreateWorkflow)
	g.GET("/:id", h.GetWorkflow)
	g.PUT("/:id", h.UpdateWorkflow)
	g.DELETE("/:id", h.DeleteWorkflow)
	g.GET("/:id/revisions", h.ListRevisions)
	g.GET("/:id/revisions/:rev", h.GetRevision)
	g.GET("/:id/webhook-token", h.GetWebhookToken)
	g.POST("/:id/webhook-token/reset", h.ResetWebhookToken)
	g.POST("/:id/runs", h.TriggerRun)
	g.GET("/:id/runs", h.ListRuns)

	runs := api.Group("/runs")
	runs.GET("/:id", h.GetRun)
	runs.GET("/:id/events", h.RunEvents)
	runs.POST("/:id/cancel", h.CancelRun)
	runs.POST("/:id/retry", h.RetryRun)
	runs.POST("/:id/nodes/:nodeId/approve", h.Decide(true))
	runs.POST("/:id/nodes/:nodeId/reject", h.Decide(false))

	// Build-cluster defaults: the empty-namespace global default row is
	// reachable at the bare path (a path parameter cannot be empty).
	settings := api.Group("/build-settings")
	settings.GET("", h.ListBuildSettings)
	settings.PUT("", h.UpsertBuildSetting)
	settings.PUT("/:namespace", h.UpsertBuildSetting)
	settings.DELETE("", h.DeleteBuildSetting)
	settings.DELETE("/:namespace", h.DeleteBuildSetting)
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

// failEngine maps engine sentinel errors onto HTTP statuses.
func (h *Handler) failEngine(c *gin.Context, err error) {
	switch {
	case errors.Is(err, engine.ErrNotFound):
		h.fail(c, http.StatusNotFound, nil, "workflow run not found")
	case errors.Is(err, engine.ErrConflict):
		h.fail(c, http.StatusConflict, nil, "run is not in a state allowing this action")
	default:
		h.fail(c, http.StatusInternalServerError, err, "")
	}
}

// findWorkflow loads a workflow by its numeric ID (the definition identity;
// names are reusable labels), writing 400 for a malformed id and 404 when
// missing.
func (h *Handler) findWorkflow(c *gin.Context) (*store.Workflow, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		h.fail(c, http.StatusBadRequest, nil, "invalid workflow id")
		return nil, false
	}
	var wf store.Workflow
	if err := h.store.DB().First(&wf, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.fail(c, http.StatusNotFound, nil, "workflow not found")
			return nil, false
		}
		h.fail(c, http.StatusInternalServerError, err, "")
		return nil, false
	}
	return &wf, true
}

// recordAudit persists an audit entry attributed to the request's user.
func (h *Handler) recordAudit(c *gin.Context, entry store.HistoryEntry) {
	entry.OperatorID = h.deps.OperatorID(c)
	entry.Success = true
	h.deps.RecordHistory(entry)
}
