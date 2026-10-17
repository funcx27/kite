package model

import (
	"errors"

	"gorm.io/gorm"
)

// DefaultRegistrySetting is the singleton row (ID pinned to 1) pointing at
// the credential center's default registry: the credential name plus the
// host key of one of its registry entries. The pointer is validated against
// the credential center on every use — a dangling pointer (credential
// deleted or host removed) reads as "no default", never an error. No secret
// material is stored here.
type DefaultRegistrySetting struct {
	Model
	Credential string `json:"credential" gorm:"type:varchar(63);not null;default:''"`
	Host       string `json:"host" gorm:"type:varchar(255);not null;default:''"`
}

func (DefaultRegistrySetting) TableName() string {
	return "default_registry_settings"
}

// GetDefaultRegistrySetting returns the singleton row, or nil when no default
// has been persisted yet.
func GetDefaultRegistrySetting() (*DefaultRegistrySetting, error) {
	var row DefaultRegistrySetting
	err := DB.First(&row, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// SaveDefaultRegistrySetting upserts the singleton row.
func SaveDefaultRegistrySetting(row *DefaultRegistrySetting) error {
	row.ID = 1
	return DB.Save(row).Error
}
