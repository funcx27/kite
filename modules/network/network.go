// Package network implements the kite network module: a domain registry
// whose routes, annotations and TLS settings are rendered into managed
// Ingress resources. It is an independent go module with zero imports of the
// host codebase; host capabilities are injected through Deps
// (TECH-DESIGN .zcode/devflow/20261021-1050 §1).
package network

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netapi "github.com/funcx27/kite/modules/network/internal/api"
	"github.com/funcx27/kite/modules/network/internal/store"
)

// HistoryEntry is one audit record for a state change performed by the
// module. The host persists it as a ResourceHistory row attributed to the
// network module.
type HistoryEntry = store.HistoryEntry

// Deps is the set of host capabilities the network module depends on, wired
// by the host-side adapter (pkg/networkadapter).
type Deps interface {
	// DB returns the host database handle for the module's own tables.
	DB() *gorm.DB
	// ResolveCluster returns the controller-runtime client of the named
	// cluster, or an error if the cluster is unknown or offline.
	ResolveCluster(ctx context.Context, name string) (client.Client, error)
	// Authorize reports whether the request may perform verb on resource in
	// cluster/namespace, judged against the host RBAC roles carried in the
	// gin context.
	Authorize(c *gin.Context, resource, verb, cluster, namespace string) bool
	// CertificateData resolves the named platform certificate to its TLS
	// material (chain, decrypted key, content hash) for the managed TLS
	// secret; an unknown certificate is an error. The key is in-memory only:
	// it must never be logged or persisted outside the upserted secret.
	CertificateData(name string) (certPem, keyPem, contentHash string, err error)
	// CertificateNotAfter resolves the named certificate's expiry for
	// display; an unknown certificate is an error.
	CertificateNotAfter(name string) (time.Time, error)
	// CertificateDomains resolves the named certificate's subject common name
	// and DNS SANs (deduplicated, at most five) for display; an unknown or
	// unparsable certificate is an error.
	CertificateDomains(name string) ([]string, error)
	// RecordHistory persists an audit entry for a state change.
	RecordHistory(entry HistoryEntry)
}

// Module wires the module's store and host dependencies.
type Module struct {
	deps  Deps
	store *store.Store
}

// New builds the module from its host dependencies.
func New(deps Deps) *Module {
	return &Module{deps: deps, store: store.New(deps.DB())}
}

// Migrate creates or updates the module tables on the host database.
func Migrate(db *gorm.DB) error {
	return store.Migrate(db)
}

// RegisterRoutes mounts the member API under /network. The host must call it
// before applying its RBAC middleware: handlers authorize requests themselves
// via Deps. Route shapes are the frozen contract (TECH-DESIGN §3).
func RegisterRoutes(api *gin.RouterGroup, deps Deps) {
	netapi.RegisterRoutes(api, deps)
}

// RegisterAdminRoutes mounts the admin API (annotation catalog writes) on the
// host-provided /network group under the admin API; the host's RequireAdmin
// middleware gates the group.
func RegisterAdminRoutes(admin *gin.RouterGroup, deps Deps) {
	netapi.RegisterAdminRoutes(admin, deps)
}
