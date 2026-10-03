package model

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
}

func (BuildProvider) TableName() string {
	return "build_providers"
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

func DeleteBuildProvider(name string) (int64, error) {
	res := DB.Where("name = ?", name).Delete(&BuildProvider{})
	return res.RowsAffected, res.Error
}
