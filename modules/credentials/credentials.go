// Package credentials implements the kite credentials module: named
// credential entries (git basic auth, registry auth) stored encrypted at
// rest, plus a by-name resolve capability the build path consumes
// (docs/demo/workflow-design.md §9). It is an independent go module with
// zero imports of the host codebase; host capabilities are injected
// through Deps.
package credentials

import (
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	credentialsapi "github.com/funcx27/kite/modules/credentials/internal/api"
	"github.com/funcx27/kite/modules/credentials/internal/store"
)

// Credential re-exports the decrypted payload shape of §9. GitCred and
// RegCred are its components.
type (
	Credential = store.Payload
	GitCred    = store.GitCred
	RegCred    = store.RegCred
)

// ErrNotFound reports that no credential carries the requested name.
var ErrNotFound = errors.New("credentials: credential not found")

// Deps is the set of host capabilities the module depends on. Deliberately
// minimal: the module stores opaque encrypted bytes and judges requests
// against the host RBAC — it needs no cluster or user capabilities.
type Deps interface {
	// Authorize reports whether the request may perform verb on the
	// credentials pseudo-resource.
	Authorize(c *gin.Context, verb string) bool
	// DB returns the host database handle for the module's own tables.
	DB() *gorm.DB
	// EncryptSecret seals plaintext with the host's at-rest encryption and
	// returns the ciphertext to persist.
	EncryptSecret(plaintext string) (string, error)
	// DecryptSecret opens ciphertext produced by EncryptSecret.
	DecryptSecret(encrypted string) (string, error)
}

// Module is a wired credentials module. One host process owns one Module.
type Module struct {
	deps  Deps
	store *store.Store
}

// New wires the module onto the host capabilities. The host must have
// called SetSecretCodec (via the adapter) before any module table is read.
func New(deps Deps) *Module {
	return &Module{deps: deps, store: store.New(deps.DB())}
}

// RegisterRoutes mounts the module API under /credentials. The host must
// call it before applying its RBAC middleware (same placement as
// workflows/builds): the URL shapes are non-resource and handlers
// authorize requests themselves via Deps. The full Deps satisfies the
// handler surface (Authorize) directly.
func (m *Module) RegisterRoutes(api *gin.RouterGroup) {
	credentialsapi.RegisterRoutes(api, m.deps, m.store)
}

// Migrate creates or updates the module tables on the host database.
func Migrate(db *gorm.DB) error {
	return store.Migrate(db)
}

// SetSecretCodec wires the SecretJSON codec to host implementations. The
// host must call it before any module table is read or written; GORM's
// Scan/Value cannot reach Deps, so the module keeps the codec as
// package-level state.
func SetSecretCodec(encrypt, decrypt func(string) (string, error)) {
	store.SetSecretCodec(encrypt, decrypt)
}

// Resolve loads the named credential and decrypts its payload for the
// build path (git source auth and the registry push table). The returned
// secret material is in-memory only: it must never be logged or persisted.
func (m *Module) Resolve(name string) (*Credential, error) {
	row, err := m.store.GetByName(name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var p Credential
	if err := row.Payload.Unmarshal(&p); err != nil {
		return nil, fmt.Errorf("credentials: unmarshal payload %q: %w", name, err)
	}
	return &p, nil
}
