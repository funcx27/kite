// Package api serves the credentials module HTTP API: CRUD over named
// credentials whose payloads are write-only — responses carry shape
// summaries, never secret material. Routes mount before the host RBAC
// middleware and authorize themselves against the "credentials"
// pseudo-resource (same self-authorization pattern as workflows/builds).
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/funcx27/kite/modules/credentials/internal/store"
)

// authzResource is the pseudo-resource all operations authorize against.
const authzResource = "credentials"

// Deps is the set of host capabilities the HTTP handlers depend on.
type Deps interface {
	// Authorize reports whether the request may perform verb on the
	// credentials pseudo-resource. Credentials are host-wide (no
	// cluster/namespace scope), so no scope arguments exist.
	Authorize(c *gin.Context, verb string) bool
}

// Handler serves the credentials module HTTP API.
type Handler struct {
	deps  Deps
	store *store.Store
}

// RegisterRoutes mounts the module API: /credentials for the CRUD surface.
func RegisterRoutes(api *gin.RouterGroup, deps Deps, st *store.Store) {
	h := &Handler{deps: deps, store: st}
	g := api.Group("/credentials")
	g.GET("", h.List)
	g.POST("", h.Create)
	g.GET("/:name", h.Get)
	g.PUT("/:name", h.Update)
	g.DELETE("/:name", h.Delete)
}

// authorize runs the module-side permission check and writes the 403
// response when access is denied.
func (h *Handler) authorize(c *gin.Context, verb string) bool {
	if h.deps.Authorize(c, verb) {
		return true
	}
	c.AbortWithStatusJSON(http.StatusForbidden,
		gin.H{"error": "user does not have permission to " + verb + " " + authzResource})
	return false
}

// fail writes the JSON error body; the err message wins when set. Handlers
// never pass payload material as err.
func (h *Handler) fail(c *gin.Context, status int, err error, message string) {
	if err != nil {
		message = err.Error()
	}
	c.JSON(status, gin.H{"error": message})
}
