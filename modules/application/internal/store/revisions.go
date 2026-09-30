package store

import (
	"fmt"

	"gorm.io/gorm"
)

// AppendRevision records cfg.Manifests as the next revision of the config and
// prunes the chain beyond MaxConfigRevisions, atomically in one transaction.
// The prune never reclaims Rev numbers, so gaps stay stable for rollback
// semantics.
func AppendRevision(db *gorm.DB, cfg *AppConfig, source string, operatorID uint) error {
	if cfg == nil || cfg.ID == 0 {
		return fmt.Errorf("append revision: config is not persisted")
	}
	_, err := AppendRevisionTx(db, cfg.ID, source, cfg.Manifests, operatorID)
	return err
}

// AppendRevisionTx is the transaction-scoped primitive: it allocates the next
// per-config Rev (MAX+1), inserts the snapshot and prunes the chain, all
// within the caller's transaction. Callers that must keep the config update
// and its snapshot atomic (design §3: every DB-side change records one full
// snapshot) pass their transaction instead of using AppendRevision.
func AppendRevisionTx(tx *gorm.DB, appConfigID uint, source string, manifests []byte, operatorID uint) (int, error) {
	var maxRev int
	if err := tx.Model(&ConfigRevision{}).
		Where("app_config_id = ?", appConfigID).
		Select("COALESCE(MAX(rev), 0)").
		Scan(&maxRev).Error; err != nil {
		return 0, fmt.Errorf("append revision: query max rev: %w", err)
	}
	rev := ConfigRevision{
		AppConfigID: appConfigID,
		Rev:         maxRev + 1,
		Source:      source,
		Manifests:   SecretJSON(manifests),
		OperatorID:  operatorID,
	}
	if err := tx.Create(&rev).Error; err != nil {
		return 0, fmt.Errorf("append revision: create: %w", err)
	}
	if err := tx.Where("app_config_id = ? AND rev <= ?", appConfigID, rev.Rev-MaxConfigRevisions).
		Delete(&ConfigRevision{}).Error; err != nil {
		return 0, fmt.Errorf("append revision: prune: %w", err)
	}
	return rev.Rev, nil
}
