package store

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Store is the module's handle on the host database.
type Store struct {
	db *gorm.DB
}

// New wraps the host database handle.
func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying database handle.
func (s *Store) DB() *gorm.DB {
	return s.db
}

// CreateHost inserts a new host row; a duplicate (cluster, host) fails with
// an error IsUniqueViolation classifies.
func (s *Store) CreateHost(h *Host) error {
	if err := s.db.Create(h).Error; err != nil {
		return fmt.Errorf("create network host %s/%s: %w", h.Cluster, h.Host, err)
	}
	return nil
}

// ListHosts returns the hosts of a space; an empty cluster spans all clusters.
func (s *Store) ListHosts(space, cluster string) ([]Host, error) {
	q := s.db.Where("namespace = ?", space)
	if cluster != "" {
		q = q.Where("cluster = ?", cluster)
	}
	var hosts []Host
	if err := q.Order("host").Find(&hosts).Error; err != nil {
		return nil, fmt.Errorf("list network hosts in %s: %w", space, err)
	}
	return hosts, nil
}

// GetHost loads the host row by its (cluster, host) identity; a missing row
// wraps gorm.ErrRecordNotFound.
func (s *Store) GetHost(cluster, host string) (*Host, error) {
	var h Host
	if err := s.db.Where("cluster = ? AND host = ?", cluster, host).First(&h).Error; err != nil {
		return nil, fmt.Errorf("get network host %s/%s: %w", cluster, host, err)
	}
	return &h, nil
}

// UpdateHost persists changes to an existing host row (matched by ID).
func (s *Store) UpdateHost(h *Host) error {
	if err := s.db.Save(h).Error; err != nil {
		return fmt.Errorf("update network host %s/%s: %w", h.Cluster, h.Host, err)
	}
	return nil
}

// DeleteHost removes the host row by its (cluster, host) identity and reports
// whether a row was removed.
func (s *Store) DeleteHost(cluster, host string) (bool, error) {
	res := s.db.Where("cluster = ? AND host = ?", cluster, host).Delete(&Host{})
	if res.Error != nil {
		return false, fmt.Errorf("delete network host %s/%s: %w", cluster, host, res.Error)
	}
	return res.RowsAffected > 0, nil
}

// BumpDesiredRevTx increments the host's desired_rev inside the caller's
// transaction, so a spec edit and its revision bump commit atomically.
func BumpDesiredRevTx(tx *gorm.DB, id uint) error {
	if err := tx.Model(&Host{}).Where("id = ?", id).
		Update("desired_rev", gorm.Expr("desired_rev + 1")).Error; err != nil {
		return fmt.Errorf("bump desired rev: %w", err)
	}
	return nil
}

// ListAnnotationKeys returns the full annotation key catalog.
func (s *Store) ListAnnotationKeys() ([]AnnotationKey, error) {
	var keys []AnnotationKey
	if err := s.db.Order("key").Find(&keys).Error; err != nil {
		return nil, fmt.Errorf("list annotation keys: %w", err)
	}
	return keys, nil
}

// GetAnnotationKey loads one catalog entry by ID; a missing row wraps
// gorm.ErrRecordNotFound.
func (s *Store) GetAnnotationKey(id uint) (*AnnotationKey, error) {
	var k AnnotationKey
	if err := s.db.First(&k, id).Error; err != nil {
		return nil, fmt.Errorf("get annotation key %d: %w", id, err)
	}
	return &k, nil
}

// CreateAnnotationKey inserts a new catalog entry; a duplicate key fails with
// an error IsUniqueViolation classifies.
func (s *Store) CreateAnnotationKey(k *AnnotationKey) error {
	if err := s.db.Create(k).Error; err != nil {
		return fmt.Errorf("create annotation key %s: %w", k.Key, err)
	}
	return nil
}

// UpdateAnnotationKey persists changes to an existing catalog entry (matched
// by ID; the key itself is immutable per the API contract).
func (s *Store) UpdateAnnotationKey(k *AnnotationKey) error {
	if err := s.db.Save(k).Error; err != nil {
		return fmt.Errorf("update annotation key %s: %w", k.Key, err)
	}
	return nil
}

// DeleteAnnotationKey removes the catalog entry by ID and reports whether a
// row was removed.
func (s *Store) DeleteAnnotationKey(id uint) (bool, error) {
	res := s.db.Delete(&AnnotationKey{}, id)
	if res.Error != nil {
		return false, fmt.Errorf("delete annotation key %d: %w", id, res.Error)
	}
	return res.RowsAffected > 0, nil
}

// ImportTx inserts annotation keys that do not already exist (matched by key)
// inside the caller's transaction; keys already present count as skipped and
// are never overwritten. Parsing and validating the import text happens in
// the handler layer; this is the persistence primitive only.
func ImportTx(tx *gorm.DB, keys []AnnotationKey) (imported, skipped int, err error) {
	for i := range keys {
		var count int64
		if err := tx.Model(&AnnotationKey{}).Where("key = ?", keys[i].Key).Count(&count).Error; err != nil {
			return 0, 0, fmt.Errorf("import annotation keys: count %s: %w", keys[i].Key, err)
		}
		if count > 0 {
			skipped++
			continue
		}
		row := keys[i]
		if err := tx.Create(&row).Error; err != nil {
			return 0, 0, fmt.Errorf("import annotation keys: create %s: %w", keys[i].Key, err)
		}
		imported++
	}
	return imported, skipped, nil
}

// IsUniqueViolation reports whether err is a unique-index violation, so
// callers can classify e.g. a duplicate (cluster, host) as a conflict. GORM
// maps duplicates to ErrDuplicatedKey only with TranslateError enabled, which
// the host does not use, so dialect messages are matched as well.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unique constraint failed"),
		strings.Contains(msg, "duplicate entry"),
		strings.Contains(msg, "duplicate key value"):
		return true
	}
	return false
}
