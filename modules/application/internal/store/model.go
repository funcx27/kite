package store

import (
	"time"
)

// Model carries the columns shared by the module's mutable tables.
type Model struct {
	ID        uint      `json:"id" gorm:"primarykey"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ConfigRevision Source values (design §3).
const (
	RevSourceRender      = "render"          // first deploy rendered from App.Spec
	RevSourceWizard      = "wizard"          // config saved from the wizard
	RevSourceDeployImage = "deploy-image"    // image changed in the deploy dialog
	RevSourceSync        = "sync"            // adopted from the cluster
	RevSourceResolve     = "resolve"         // drift resolution
	RevSourceCopy        = "copy"            // copied to another cluster
	RevSourceYAML        = "yaml"            // full-manifests YAML edit
	RevSourceRollback    = "rollback"        // rolled back to a revision
	RevSourcePullCred    = "pull-credential" // re-rendered for a pull credential change
)

// AppConfig StatusSummary values, written back after the detail-page drift
// check; the list page aggregates these persisted values (design §3).
const (
	StatusSynced     = "synced"
	StatusDrift      = "drift"
	StatusUndeployed = "undeployed"
	StatusError      = "error"
)

// MaxConfigRevisions is the number of revisions kept per AppConfig; older
// rows are pruned by a single DELETE after each insert.
const MaxConfigRevisions = 50

// RegistryAuth is one registry host's basic-auth pair, resolved from a
// credential by Deps.PullSecretData. Secret material is in-memory only and
// must never be logged or persisted outside the upserted pull secret.
type RegistryAuth struct {
	Username string
	Password string
}

// App is a logical application: a grouping plus the draft spec produced by
// the creation wizard. It is cluster-independent and unique by name.
type App struct {
	Model
	Name        string    `json:"name" gorm:"type:varchar(63);uniqueIndex;not null"` // DNS-1123
	Namespace   string    `json:"namespace" gorm:"type:varchar(255);not null"`       // default space of the app's configs
	Description string    `json:"description" gorm:"type:text"`
	Spec        JSONField `json:"spec" gorm:"type:text"` // AppSpec draft; birth snapshot once deployed

	// Configs association enables Preload and the FK cascade below.
	Configs []AppConfig `json:"-" gorm:"foreignKey:AppID;constraint:OnDelete:CASCADE"`
}

func (App) TableName() string {
	return "apps"
}

// AppConfig is the per-cluster desired configuration of an app; at most one
// row per (AppID, Cluster).
type AppConfig struct {
	Model
	AppID     uint   `json:"appId" gorm:"not null;uniqueIndex:idx_app_configs_app_cluster,priority:1"`
	Cluster   string `json:"cluster" gorm:"type:varchar(100);not null;uniqueIndex:idx_app_configs_app_cluster,priority:2"`
	Namespace string `json:"namespace" gorm:"type:varchar(255);not null"`

	// Manifests and LastApplied hold Secret data/stringData values, so both
	// are encrypted at rest with KITE_ENCRYPT_KEY like revision snapshots
	// (design §9); they arrive decrypted through GORM's Scanner.
	Manifests SecretJSON `json:"manifests" gorm:"type:text"` // desired config, multi-doc
	// LastApplied is the cluster-side baseline of the workflow publish gate,
	// refreshed after deploy/sync/resolve.
	LastApplied SecretJSON `json:"lastApplied" gorm:"type:text"`

	// CredentialRef names the credential whose registry entries kite upserts
	// into the namespace as a managed image pull secret before every publish;
	// empty disables it. It never rides the manifests, so it stays outside
	// the anchor and the drift set.
	CredentialRef string `json:"credentialRef" gorm:"type:varchar(63);not null;default:''"`

	StatusSummary   string     `json:"statusSummary" gorm:"type:varchar(20);not null;default:'';index"`
	StatusCheckedAt *time.Time `json:"statusCheckedAt"`

	// ReadyReplicas/Replicas snapshot the live workload's replica counts during
	// the status check; nil means undetected or no workload.
	ReadyReplicas *int64 `json:"readyReplicas"`
	Replicas      *int64 `json:"replicas"`

	LastDeployedAt *time.Time `json:"lastDeployedAt"`
	LastSyncedAt   *time.Time `json:"lastSyncedAt"`

	// App association exists for the FK cascade: deleting an app removes all
	// its cluster configs and revision chains (constraint declared on App.Configs).
	App *App `json:"app,omitempty" gorm:"foreignKey:AppID"`

	// Revisions association carries the FK cascade that clears the config's
	// revision chain when the config row is deleted (design §3).
	Revisions []ConfigRevision `json:"-" gorm:"foreignKey:AppConfigID;constraint:OnDelete:CASCADE"`
}

func (AppConfig) TableName() string {
	return "app_configs"
}

// ConfigRevision is the per-config version snapshot written on every DB-side
// change; Rev is monotonic per AppConfig and gaps are never reused.
type ConfigRevision struct {
	ID          uint   `json:"id" gorm:"primarykey"`
	AppConfigID uint   `json:"appConfigId" gorm:"not null;uniqueIndex:idx_config_revisions_config_rev,priority:1"`
	Rev         int    `json:"rev" gorm:"not null;uniqueIndex:idx_config_revisions_config_rev,priority:2"`
	Source      string `json:"source" gorm:"type:varchar(20);not null"`
	// RefRev marks the revision a derived one was built from (rollback: the
	// rev its content came from); 0 when not applicable.
	RefRev     int        `json:"refRev" gorm:"not null;default:0"`
	Manifests  SecretJSON `json:"manifests" gorm:"type:text"`                 // snapshot encrypted at rest (Secret values)
	OperatorID uint       `json:"operatorId" gorm:"not null;index;default:0"` // no FK: history survives user deletion
	CreatedAt  time.Time  `json:"createdAt"`
}

func (ConfigRevision) TableName() string {
	return "config_revisions"
}
