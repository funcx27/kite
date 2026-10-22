package store

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "network.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

func TestMigrateCreatesTables(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, table := range []string{"network_hosts", "network_annotation_keys"} {
		if !db.Migrator().HasTable(table) {
			t.Errorf("table %s not created", table)
		}
	}
}

func assertSeedCount(t *testing.T, db *gorm.DB, want int64) {
	t.Helper()
	var got int64
	if err := db.Model(&AnnotationKey{}).Count(&got).Error; err != nil {
		t.Fatalf("count annotation keys: %v", err)
	}
	if got != want {
		t.Errorf("annotation key count = %d, want %d", got, want)
	}
}

func TestMigrateSeedsCatalog(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, seed := range seedAnnotationKeys {
		var row AnnotationKey
		if err := db.Where("key = ?", seed.Key).First(&row).Error; err != nil {
			t.Errorf("seed %s missing: %v", seed.Key, err)
			continue
		}
		if row.Description != seed.Description || row.IngressClass != seed.IngressClass {
			t.Errorf("seed %s = %+v, want %+v", seed.Key, row, seed)
		}
	}
	assertSeedCount(t, db, int64(len(seedAnnotationKeys)))
}

func TestMigrateSeedIdempotency(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, db *gorm.DB)
	}{
		{
			name: "second migrate does not duplicate seeds",
			run: func(t *testing.T, db *gorm.DB) {
				if err := Migrate(db); err != nil {
					t.Fatalf("second migrate: %v", err)
				}
				assertSeedCount(t, db, int64(len(seedAnnotationKeys)))
			},
		},
		{
			name: "second migrate does not overwrite edited rows",
			run: func(t *testing.T, db *gorm.DB) {
				const key = "konghq.com/strip-path"
				if err := db.Model(&AnnotationKey{}).Where("key = ?", key).
					Update("description", "admin edited").Error; err != nil {
					t.Fatalf("edit seed row: %v", err)
				}
				if err := Migrate(db); err != nil {
					t.Fatalf("second migrate: %v", err)
				}
				var row AnnotationKey
				if err := db.Where("key = ?", key).First(&row).Error; err != nil {
					t.Fatalf("reload seed row: %v", err)
				}
				if row.Description != "admin edited" {
					t.Errorf("seed overwritten: description = %q, want %q", row.Description, "admin edited")
				}
				assertSeedCount(t, db, int64(len(seedAnnotationKeys)))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			if err := Migrate(db); err != nil {
				t.Fatalf("first migrate: %v", err)
			}
			tt.run(t, db)
		})
	}
}
