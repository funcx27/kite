package model

import "github.com/zxh326/kite/pkg/common"

// UserDirectoryEntry is the minimal identity a picker needs — no account
// details beyond id/username.
type UserDirectoryEntry struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
}

// ListUserDirectory returns selectable human accounts ordered by username.
// API key identities and disabled users are excluded: neither can log in to
// approve a run.
func ListUserDirectory() ([]UserDirectoryEntry, error) {
	var entries []UserDirectoryEntry
	err := DB.Model(&User{}).
		Where("provider != ? AND enabled = ?", common.APIKeyProvider, true).
		Order("username").
		Select("id", "username").
		Find(&entries).Error
	return entries, err
}
