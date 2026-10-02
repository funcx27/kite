// Package workflow implements the kite workflow module: DAG pipeline
// definitions with revision chains, an in-process scheduling engine and the
// HTTP API. It is an independent go module with zero imports of the host
// codebase; host capabilities are injected through Deps
// (design docs/demo/workflow-design.md §1/§2).
package workflow

import (
	"context"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/funcx27/kite/modules/workflow/exec"
	applicationapi "github.com/funcx27/kite/modules/workflow/internal/api"
	"github.com/funcx27/kite/modules/workflow/internal/engine"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// HistoryEntry is one audit record for a state change performed by the
// module. The host persists it as a ResourceHistory row attributed to the
// workflow module.
type HistoryEntry = store.HistoryEntry

// Deps is the set of host capabilities the module depends on. It is one
// injection point by design (docs/demo/workflow-design.md §2); the methods
// mirror the application module's Deps plus the three host capability ports
// of §5 (BuildImage/DeployApp/Notify, consumed by the engine's built-in node
// runners). Adding a node type needs only an engine runner; extend Deps only
// when a node needs a new host capability.
type Deps interface {
	// Authorize reports whether the request may perform verb on resource in
	// cluster/namespace, judged against the host RBAC roles carried in the
	// gin context.
	Authorize(c *gin.Context, resource, verb, cluster, namespace string) bool
	// AuthorizeUser reports whether the given user may perform verb on
	// resource in cluster/namespace. Runs execute with their trigger user's
	// RBAC identity (design §7), so engine node dispatch checks by user id —
	// there is no gin context outside a request (design §8 节点级映射).
	AuthorizeUser(userID uint, resource, verb, cluster, namespace string) bool
	// OperatorID returns the ID of the authenticated user of the request.
	OperatorID(c *gin.Context) uint
	// UserNames resolves user IDs to display names; deleted or unknown IDs
	// are simply absent from the map.
	UserNames(ids []uint) map[uint]string
	// IsAdmin reports whether the request's authenticated user holds the
	// host admin role; approval nodes accept any assignee or an admin
	// (design §8).
	IsAdmin(c *gin.Context) bool
	// RecordHistory persists an audit entry for a state change.
	RecordHistory(entry HistoryEntry)
	// DB returns the host database handle for the module's own tables.
	DB() *gorm.DB
	// EncryptSecret seals plaintext with the host's at-rest encryption and
	// returns the ciphertext to persist.
	EncryptSecret(plaintext string) (string, error)
	// DecryptSecret opens ciphertext produced by EncryptSecret.
	DecryptSecret(encrypted string) (string, error)

	// BuildImage runs one image build (design §5.1). onLog receives raw
	// build output chunks for the node's LogTail and the run's SSE stream.
	BuildImage(ctx context.Context, req exec.BuildRequest, onLog func(string)) (exec.BuildResult, error)
	// DeployApp publishes an image through the application module's deploy
	// semantics, gates included (design §5.2). onLog receives the publish
	// header, per-document apply results and conflict details for the node's
	// LogTail and the run's SSE stream.
	DeployApp(ctx context.Context, req exec.DeployRequest, onLog func(string)) (exec.DeployResult, error)
	// Notify delivers one webhook notification (design §5.4).
	Notify(ctx context.Context, req exec.NotifyRequest) error
}

// Module is a wired workflow module: handlers plus the in-process engine.
// One host process owns one Module.
type Module struct {
	deps   Deps
	store  *store.Store
	engine *engine.Engine
}

// New wires the module onto the host capabilities. The host must have called
// SetSecretCodec (via the adapter) before any module table is read.
func New(deps Deps) *Module {
	st := store.New(deps.DB())
	return &Module{deps: deps, store: st, engine: engine.New(deps.DB(), deps)}
}

// RegisterRoutes mounts the module API under /workflows and /runs. The host
// must call it before applying its RBAC middleware (same placement as
// applications/builds): the URL shapes are non-resource and handlers
// authorize requests themselves via Deps.
func (m *Module) RegisterRoutes(api *gin.RouterGroup) {
	applicationapi.RegisterRoutes(api, handlerDeps{m.deps}, m.store, m.engine)
}

// Start resumes in-flight runs after a process restart (design §6 重启恢复):
// running task nodes are marked interrupted, approval waits are preserved,
// and every non-terminal run is re-driven. The host calls it once after
// Migrate.
func (m *Module) Start() error {
	return m.engine.Start()
}

// Migrate creates or updates the module tables on the host database.
func Migrate(db *gorm.DB) error {
	return store.Migrate(db)
}

// SetSecretCodec wires the SecretString codec to host implementations. The
// host must call it before any module table is read or written; GORM's
// Scan/Value cannot reach Deps, so the module keeps the codec as
// package-level state.
func SetSecretCodec(encrypt, decrypt func(string) (string, error)) {
	store.SetSecretCodec(encrypt, decrypt)
}

// handlerDeps projects the full Deps onto the authorize/audit surface the
// HTTP handlers use.
type handlerDeps struct{ d Deps }

func (h handlerDeps) Authorize(c *gin.Context, resource, verb, cluster, namespace string) bool {
	return h.d.Authorize(c, resource, verb, cluster, namespace)
}

func (h handlerDeps) OperatorID(c *gin.Context) uint { return h.d.OperatorID(c) }

func (h handlerDeps) UserNames(ids []uint) map[uint]string { return h.d.UserNames(ids) }

func (h handlerDeps) IsAdmin(c *gin.Context) bool { return h.d.IsAdmin(c) }

func (h handlerDeps) RecordHistory(entry store.HistoryEntry) { h.d.RecordHistory(entry) }

// The injected Deps always satisfies the engine's full surface (host
// capability ports plus identity-based authorization).
var _ engine.Deps = (Deps)(nil)
