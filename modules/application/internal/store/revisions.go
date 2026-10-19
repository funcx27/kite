package store

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
)

// AppendRevisionTx is the transaction-scoped primitive: it allocates the next
// per-config Rev (MAX+1), inserts the snapshot and prunes the chain, all
// within the caller's transaction, so the config update and its snapshot stay
// atomic (design §3: every DB-side change records one full snapshot). A
// snapshot canonically identical to the chain's newest revision appends
// nothing and reports that rev — republishing an unchanged set never forks a
// duplicate Rev pair the history diff would render as two identical columns.
// refRev marks the revision a derived snapshot was built from (rollback: the
// rev its content came from); 0 when not applicable.
func AppendRevisionTx(tx *gorm.DB, appConfigID uint, source string, manifests []byte, operatorID uint, refRev int) (int, error) {
	var maxRev int
	if err := tx.Model(&ConfigRevision{}).
		Where("app_config_id = ?", appConfigID).
		Select("COALESCE(MAX(rev), 0)").
		Scan(&maxRev).Error; err != nil {
		return 0, fmt.Errorf("append revision: query max rev: %w", err)
	}
	if maxRev > 0 {
		var latest ConfigRevision
		if err := tx.Where("app_config_id = ? AND rev = ?", appConfigID, maxRev).
			First(&latest).Error; err != nil {
			return 0, fmt.Errorf("append revision: load latest: %w", err)
		}
		if ManifestsEqual(latest.Manifests, manifests) {
			return maxRev, nil
		}
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

// ManifestsEqual compares two manifest payloads canonically: parsed JSON
// re-marshals with sorted keys, so key order and formatting differences are
// not content changes. Undecodable bytes fall back to a plain byte compare.
func ManifestsEqual(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	na, errA := canonicalJSON(a)
	nb, errB := canonicalJSON(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(na, nb)
}

func canonicalJSON(data []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
