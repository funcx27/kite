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
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

// HistoryEntry is one audit record for a state change performed by the
// module. The host persists it as a ResourceHistory row attributed to the
// application module.
type HistoryEntry = store.HistoryEntry

// RegistryAuth is one registry host's basic-auth pair, resolved from a
// credential by Deps.PullSecretData.
type RegistryAuth = store.RegistryAuth

// Deps is the set of host capabilities the module depends on. It is one
// injection point by design (docs/demo/design.md §1); the methods stay
// grouped here instead of being fragmented per capability.
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
	// resource in cluster/namespace. The deploy facade (DeployWithImage)
	// runs outside any request — workflow nodes execute with their trigger
	// user's identity — so its permission checks go by user id; unknown
	// users are denied.
	AuthorizeUser(userID uint, resource, verb, cluster, namespace string) bool
	// OperatorID returns the ID of the authenticated user of the request.
	OperatorID(c *gin.Context) uint
	// UserNames resolves user IDs to display names for history views; deleted
	// or unknown IDs are simply absent from the map.
	UserNames(ids []uint) map[uint]string
	// RecordHistory persists an audit entry for a state change.
	RecordHistory(entry HistoryEntry)
	// DB returns the host database handle for the module's own tables.
	DB() *gorm.DB
	// EncryptSecret seals plaintext with the host's at-rest encryption and
	// returns the ciphertext to persist.
	EncryptSecret(plaintext string) (string, error)
	// DecryptSecret opens ciphertext produced by EncryptSecret.
	DecryptSecret(encrypted string) (string, error)
	// PullSecretData resolves a credentialRef to its registry auth table
	// (host -> user/pass) for the managed image pull secret; git entries are
	// ignored. An unknown credentialRef is an error.
	PullSecretData(credentialRef string) (map[string]store.RegistryAuth, error)
	// CertificateData resolves the named platform certificate to its TLS
	// material: the normalized chain, the decrypted private key and the
	// content hash the publish pipeline stamps onto managed TLS secrets. An
	// unknown certificate is an error. The key is in-memory only: it must
	// never be logged or persisted outside the upserted secret.
	CertificateData(name string) (certPem, keyPem, contentHash string, err error)
	// CertificateHash resolves only the named certificate's content hash for
	// rotation checks; it must not load or decrypt the private key.
	CertificateHash(name string) (string, error)
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
	if err := store.Migrate(db); err != nil {
		return err
	}
	backfillAppliedRev(db)
	return nil
}

// backfillAppliedRev fills the applied-rev marker for configs published before
// the column existed: the newest revision whose content matches the stored
// anchor is what the cluster last ran (the same comparison the status check
// calls synced). Best-effort on purpose — the marker is display-only
// bookkeeping that every publish/sync/resolve maintains from here on, so a
// row that fails to decode or matches no revision keeps 0 (shown as "no
// marker") rather than failing the host boot; it is retried on the next start.
func backfillAppliedRev(db *gorm.DB) {
	var configs []store.AppConfig
	if err := db.Where("last_applied <> '' AND applied_rev = 0").Find(&configs).Error; err != nil {
		return
	}
	for i := range configs {
		cfg := &configs[i]
		anchor, err := store.DecodeManifests(cfg.LastApplied)
		if err != nil {
			continue
		}
		var revs []store.ConfigRevision
		if err := db.Where("app_config_id = ?", cfg.ID).Order("rev DESC").Find(&revs).Error; err != nil {
			continue
		}
		for _, r := range revs {
			docs, err := store.DecodeManifests(r.Manifests)
			if err != nil {
				continue
			}
			if len(syncops.DiffManifests(docs, anchor)) != 0 {
				continue
			}
			db.Model(&store.AppConfig{}).Where("id = ?", cfg.ID).Update("applied_rev", r.Rev)
			break
		}
	}
}

// SetSecretCodec wires the SecretJSON codec to host implementations. The host
// must call it before any module table is read or written; GORM's Scan/Value
// cannot reach Deps, so the module keeps the codec as package-level state.
func SetSecretCodec(encrypt, decrypt func(string) (string, error)) {
	store.SetSecretCodec(encrypt, decrypt)
}
