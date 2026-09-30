// Package application implements the kite application module: logical apps,
// per-cluster configurations and their revision chains. It is an independent
// go module with zero imports of the host codebase; host capabilities are
// injected through Deps (design docs/demo/design.md §2).
package application

import (
	"context"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"sigs.k8s.io/controller-runtime/pkg/client"

	applicationapi "github.com/funcx27/kite/modules/application/internal/api"
	"github.com/funcx27/kite/modules/application/internal/store"
)

// HistoryEntry is one audit record for a state change performed by the
// module. The host persists it as a ResourceHistory row attributed to the
// application module.
type HistoryEntry = store.HistoryEntry

// Deps is the set of host capabilities the module depends on. It is one
// injection point by design (docs/demo/design.md §1); the methods stay
// grouped here instead of being fragmented per capability.
type Deps interface {
	// ResolveCluster returns the controller-runtime client of the named
	// cluster, or an error if the cluster is unknown or offline.
	ResolveCluster(ctx context.Context, name string) (client.Client, error)
	// Authorize reports whether the request may perform verb on resource in
	// cluster/namespace, judged against the host RBAC roles carried in the
	// gin context.
	Authorize(c *gin.Context, resource, verb, cluster, namespace string) bool
	// OperatorID returns the ID of the authenticated user of the request.
	OperatorID(c *gin.Context) uint
	// RecordHistory persists an audit entry for a state change.
	RecordHistory(entry HistoryEntry)
	// DB returns the host database handle for the module's own tables.
	DB() *gorm.DB
	// EncryptSecret seals plaintext with the host's at-rest encryption and
	// returns the ciphertext to persist.
	EncryptSecret(plaintext string) (string, error)
	// DecryptSecret opens ciphertext produced by EncryptSecret.
	DecryptSecret(encrypted string) (string, error)
}

// RegisterRoutes mounts the module API under /applications. The host must call
// it before applying its RBAC middleware (same placement as resources/apply):
// the URL shapes are non-resource and handlers authorize requests themselves
// via Deps. Routes follow the design §7 CRUD table.
func RegisterRoutes(api *gin.RouterGroup, deps Deps) {
	applicationapi.RegisterRoutes(api, deps, store.New(deps.DB()))
}

// Migrate creates or updates the module tables on the host database.
func Migrate(db *gorm.DB) error {
	return store.Migrate(db)
}

// SetSecretCodec wires the SecretJSON codec to host implementations. The host
// must call it before any module table is read or written; GORM's Scan/Value
// cannot reach Deps, so the module keeps the codec as package-level state.
func SetSecretCodec(encrypt, decrypt func(string) (string, error)) {
	store.SetSecretCodec(encrypt, decrypt)
}
