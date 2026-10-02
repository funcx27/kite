package store

import (
	"fmt"

	"gorm.io/gorm"
)

// AppendRevisionTx is the transaction-scoped primitive: it allocates the next
// per-config Rev (MAX+1), inserts the snapshot and prunes the chain, all
// within the caller's transaction, so the config update and its snapshot stay
// atomic (design §3: every DB-side change records one full snapshot). refRev
// marks the revision a derived snapshot was built from (rollback: the rev its
// content came from); 0 when not applicable.
func AppendRevisionTx(tx *gorm.DB, appConfigID uint, source string, manifests []byte, operatorID uint, refRev int) (int, error) {
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
		RefRev:      refRev,
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
