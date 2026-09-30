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
	return nil
}
