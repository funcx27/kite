package store

import (
	"fmt"

	"gorm.io/gorm"
)

// Migrate creates or updates the module tables on the host database.
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&App{}, &AppConfig{}, &ConfigRevision{}); err != nil {
		return fmt.Errorf("migrate application models: %w", err)
	}
	// AutoMigrate never drops indexes removed from the struct tags, so the
	// legacy name-only uniqueness must be dropped here or same-name apps in
	// different spaces keep colliding.
	if db.Migrator().HasIndex(&App{}, "idx_apps_name") {
		if err := db.Migrator().DropIndex(&App{}, "idx_apps_name"); err != nil {
			return fmt.Errorf("drop legacy apps name index: %w", err)
		}
	}
	return nil
}
