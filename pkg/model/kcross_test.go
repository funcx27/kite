package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupKcrossTestDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("opening test database: %v", err)
	}
	if err := db.AutoMigrate(&Cluster{}, &KcrossBinding{}); err != nil {
		t.Fatalf("migrating test database: %v", err)
	}
	originalDB := DB
	DB = db
	t.Cleanup(func() { DB = originalDB })
}

func TestDeleteClusterCleansKcrossBinding(t *testing.T) {
	tests := []struct {
		name    string
		binding bool
	}{
		{"bound cluster", true},
		{"cluster without binding", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupKcrossTestDB(t)

			cluster := &Cluster{Name: "poc"}
			if err := AddCluster(cluster); err != nil {
				t.Fatalf("add cluster: %v", err)
			}
			if tt.binding {
				if _, err := UpsertKcrossBinding(cluster.ID, "peer-1", "kubernetes"); err != nil {
					t.Fatalf("upsert binding: %v", err)
				}
			}

			if err := DeleteCluster(cluster); err != nil {
				t.Fatalf("delete cluster: %v", err)
			}
			if binding := GetKcrossBindingByCluster(cluster.ID); binding != nil {
				t.Fatalf("kcross binding survived cluster delete: %+v", binding)
			}
		})
	}
}
