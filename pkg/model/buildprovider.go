package model

import (
	"errors"

	"gorm.io/gorm"
)

// BuildProvider is a kite-managed k8s build provider: created in the settings
// UI from a managed cluster and injected into the buildfleet runtime without
// a config change or restart. The cluster's kubeconfig is exported to a
// 0600 file the provider points at; no secret material is stored here.
type BuildProvider struct {
	Model
	// Name is the fleet provider name; DNS-1123, unique across config-file
	// and managed providers (the create API rejects config collisions).
	Name string `json:"name" gorm:"type:varchar(63);uniqueIndex;not null"`
	// Cluster is the kite cluster name the provider builds on.
	Cluster string `json:"cluster" gorm:"type:varchar(100);not null"`
	// Namespace receives the buildkitd runtime pods.
	Namespace string `json:"namespace" gorm:"type:varchar(63);not null"`
	// Optional capacity caps; zero values fall back to the namespace
	// ResourceQuota. Memory is a quantity string, e.g. 16Gi.
	CPU    float64 `json:"cpu" gorm:"not null;default:0"`
	Memory string  `json:"memory" gorm:"type:varchar(32);not null;default:''"`
	// Runtime names the buildfleet runtime profile (a config runtimes key);
	// empty = "default".
	Runtime string `json:"runtime" gorm:"type:varchar(63);not null;default:''"`
	// ReadyTimeoutSeconds overrides the buildkitd pod-Ready poll timeout;
	// 0 = the module default (60s).
	ReadyTimeoutSeconds int `json:"readyTimeoutSeconds" gorm:"not null;default:0"`
	// PodCPU / PodMemoryGi shape the per-instance buildkitd pod (cores /
	// GiB); 0 = the built-in default (2 cores / 4Gi). Distinct from
	// CPU/Memory above, which gate the fleet's total capacity.
	PodCPU      float64 `json:"podCpu" gorm:"not null;default:0"`
	PodMemoryGi float64 `json:"podMemoryGi" gorm:"not null;default:0"`
	// Cache configures the provider's in-cluster layer-cache registry;
	// nil = disabled. Managed providers bypass the config file's
	// providerDefaults merge, so an enabled cache must carry its fields
	// complete.
	Cache *BuildProviderCache `json:"cache" gorm:"serializer:json;type:text"`
}

// BuildProviderCache mirrors buildfleet's k8s cache-registry block.
type BuildProviderCache struct {
	Enabled      bool   `json:"enabled"`
	Image        string `json:"image"`
	StorageClass string `json:"storageClass"`
	Size         string `json:"size"`
	GCSchedule   string `json:"gcSchedule"`
}

func (BuildProvider) TableName() string {
	return "build_providers"
}

// BuildRuntimeHost is one static /etc/hosts entry for buildkitd instances.
type BuildRuntimeHost struct {
	IP    string   `json:"ip"`
	Names []string `json:"names"`
}

// BuildRuntimeSetting is the singleton row (ID pinned to 1) overriding the
// buildfleet default runtime profile's hosts/registry wiring. It keeps
// kite-side structures; the buildfleet adapter maps them onto the module's
// config types.
type BuildRuntimeSetting struct {
	Model
	Hosts              []BuildRuntimeHost  `json:"hosts" gorm:"serializer:json;type:text"`
	InsecureRegistries []string            `json:"insecureRegistries" gorm:"serializer:json;type:text"`
	Mirrors            map[string][]string `json:"mirrors" gorm:"serializer:json;type:text"`
}

func (BuildRuntimeSetting) TableName() string {
	return "build_runtime_settings"
}

// GetBuildRuntimeSetting returns the singleton row, or nil when no override
// has been persisted yet.
func GetBuildRuntimeSetting() (*BuildRuntimeSetting, error) {
	var row BuildRuntimeSetting
	err := DB.First(&row, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// SaveBuildRuntimeSetting upserts the singleton row.
func SaveBuildRuntimeSetting(row *BuildRuntimeSetting) error {
	row.ID = 1
	return DB.Save(row).Error
}

func ListBuildProviders() ([]BuildProvider, error) {
	var rows []BuildProvider
	err := DB.Order("name").Find(&rows).Error
	return rows, err
}

func GetBuildProviderByName(name string) (*BuildProvider, error) {
	var row BuildProvider
	if err := DB.Where("name = ?", name).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func AddBuildProvider(row *BuildProvider) error {
	return DB.Create(row).Error
}

func UpdateBuildProvider(row *BuildProvider) error {
	return DB.Save(row).Error
}

func DeleteBuildProvider(name string) (int64, error) {
	res := DB.Where("name = ?", name).Delete(&BuildProvider{})
	return res.RowsAffected, res.Error
}
