// Package api serves the network module's HTTP API: the frozen contract of
// TECH-DESIGN §3 (hosts, publish, options, annotation catalog).
package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/network/internal/store"
)

// The RBAC resources module operations authorize against: real k8s resources,
// so existing host roles carry usable rules (TECH-DESIGN §5).
const (
	authzResource     = "ingresses"
	secretsResource   = "secrets"
	servicesResource  = "services"
	namespaceResource = "namespaces"
)

// Deps is the set of host capabilities the HTTP handlers depend on. The module
// root's network.Deps satisfies it structurally; it is restated here because
// importing the parent package from internal would cycle.
type Deps interface {
	DB() *gorm.DB
	ResolveCluster(ctx context.Context, name string) (client.Client, error)
	Authorize(c *gin.Context, resource, verb, cluster, namespace string) bool
	CertificateData(name string) (certPem, keyPem, contentHash string, err error)
	CertificateNotAfter(name string) (time.Time, error)
	CertificateDomains(name string) ([]string, error)
	RecordHistory(entry store.HistoryEntry)
}

// Handler serves the network module HTTP API.
type Handler struct {
	deps  Deps
	store *store.Store
}

func newHandler(deps Deps) *Handler {
	return &Handler{deps: deps, store: store.New(deps.DB())}
}

// RegisterRoutes mounts the member API under /network. Path params are
// deliberately not named "cluster": the host ClusterMiddleware resolves any
// :cluster path param and aborts with 404 before the handler runs, but this
// module resolves clusters itself (a host row may outlive its cluster).
func RegisterRoutes(api *gin.RouterGroup, deps Deps) {
	h := newHandler(deps)
	g := api.Group("/network")
	g.GET("/annotation-keys", h.ListAnnotationKeys)
	g.GET("/ingressclasses/:targetCluster", h.ListIngressClasses)
	g.GET("/services/:targetCluster/:namespace", h.ListServices)
	g.GET("/:space/hosts", h.ListHosts)
	g.POST("/:space/hosts", h.CreateHost)
	g.PATCH("/:space/hosts/:hostCluster/:host", h.UpdateHostSettings)
	g.PUT("/:space/hosts/:hostCluster/:host/routes", h.ReplaceRoutes)
	g.PUT("/:space/hosts/:hostCluster/:host/annotations", h.ReplaceAnnotations)
	g.POST("/:space/hosts/:hostCluster/:host/publish", h.PublishHost)
	g.POST("/:space/hosts/:hostCluster/:host/sync-cert", h.SyncCert)
	g.GET("/:space/hosts/:hostCluster/:host/export", h.ExportHost)
	g.DELETE("/:space/hosts/:hostCluster/:host", h.OfflineHost)
}

// RegisterAdminRoutes mounts the admin API (annotation catalog writes) on the
// host-provided /network group behind the host's RequireAdmin middleware.
func RegisterAdminRoutes(admin *gin.RouterGroup, deps Deps) {
	h := newHandler(deps)
	admin.POST("/annotation-keys", h.CreateAnnotationKey)
	admin.PUT("/annotation-keys/:id", h.UpdateAnnotationKey)
	admin.DELETE("/annotation-keys/:id", h.DeleteAnnotationKey)
	admin.POST("/annotation-keys/import", h.ImportAnnotationKeys)
}

// identity is the acting principal of a mutating operation: the audit operator
// plus the permission check.
type identity struct {
	operatorID uint
	authorize  func(resource, verb, cluster, namespace string) bool
}

// requestIdentity binds the identity to the request's authenticated user.
func (h *Handler) requestIdentity(c *gin.Context) identity {
	return identity{
		operatorID: operatorID(c),
		authorize: func(resource, verb, cluster, namespace string) bool {
			return h.deps.Authorize(c, resource, verb, cluster, namespace)
		},
	}
}

// operatorID extracts the numeric user id from the auth middleware's "user"
// context value. The module boundary forbids importing the host model and the
// Deps surface has no accessor, so the id is read reflectively off the struct
// the host stores (a model.User with a promoted ID field).
func operatorID(c *gin.Context) uint {
	v, ok := c.Get("user")
	if !ok {
		return 0
	}
	id := reflect.ValueOf(v).FieldByName("ID")
	if !id.IsValid() || id.Kind() != reflect.Uint {
		return 0
	}
	return uint(id.Uint())
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

// controllerFamily resolves the host's ingress class to its controller family
// on the cluster; "" when the cluster or class is unresolvable (callers then
// render family-neutrally and validate by name only).
func (h *Handler) controllerFamily(ctx context.Context, cluster, class string) string {
	return newClusterClients(ctx, h.deps).familyOf(cluster, class)
}

// findHost loads the host row by its path (space, cluster, host), writing the
// 404 response when the row is missing or registered in another space.
func (h *Handler) findHost(c *gin.Context) *store.Host {
	row, err := h.store.GetHost(c.Param("hostCluster"), c.Param("host"))
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		h.fail(c, http.StatusNotFound, nil, "network host not found")
	case err != nil:
		h.fail(c, http.StatusInternalServerError, err, "")
	case row.Namespace != c.Param("space"):
		h.fail(c, http.StatusNotFound, nil, "network host not found")
	default:
		return row
	}
	return nil
}

// recordAudit persists an audit entry attributed to the acting identity.
func (h *Handler) recordAudit(id identity, entry store.HistoryEntry) {
	entry.OperatorID = id.operatorID
	entry.Success = true
	h.deps.RecordHistory(entry)
}

// recordOperation persists an audit entry with an explicit outcome: publish
// and offline also record their failures.
func (h *Handler) recordOperation(id identity, entry store.HistoryEntry, success bool, errMsg string) {
	entry.OperatorID = id.operatorID
	entry.Success = success
	entry.Err = errMsg
	h.deps.RecordHistory(entry)
}

// isConcurrentWriteError reports transaction failures caused by a concurrent
// writer (unique-index race or sqlite BUSY), which map to a refresh-and-retry
// 409. Mirrors the application module's configs_api pattern.
func isConcurrentWriteError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unique constraint failed"),
		strings.Contains(msg, "duplicate entry"),
		strings.Contains(msg, "duplicate key value"),
		strings.Contains(msg, "database is locked"),
		strings.Contains(msg, "sqlite_busy"):
		return true
	}
	return false
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
