package store

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ErrNotFound reports that no credential carries the requested name.
var ErrNotFound = errors.New("credentials: credential not found")

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

// Migrate creates or updates the module tables on the host database.
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&Credential{}); err != nil {
		return fmt.Errorf("migrate credentials models: %w", err)
	}
	return nil
}

// List returns every credential ordered by name. Payloads decrypt in
// memory; callers must only serve shape summaries of these rows.
func (s *Store) List() ([]Credential, error) {
	var rows []Credential
	if err := s.db.Order("name").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	return rows, nil
}

// GetByName loads the named credential.
func (s *Store) GetByName(name string) (*Credential, error) {
	var row Credential
	err := s.db.Where("name = ?", name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get credential %s: %w", name, err)
	}
	return &row, nil
}

// HasName reports whether name is already taken: pre-checked before create
// and re-checked after a failed create, so the API never matches
// driver-specific uniqueness error strings.
func (s *Store) HasName(name string) (bool, error) {
	var count int64
	if err := s.db.Model(&Credential{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return false, fmt.Errorf("count credentials named %s: %w", name, err)
	}
	return count > 0, nil
}

// Create persists a new credential row.
func (s *Store) Create(row *Credential) error {
	if err := s.db.Create(row).Error; err != nil {
		return fmt.Errorf("create credential %s: %w", row.Name, err)
	}
	return nil
}

// Save persists mutations of an existing credential row.
func (s *Store) Save(row *Credential) error {
	if err := s.db.Save(row).Error; err != nil {
		return fmt.Errorf("save credential %s: %w", row.Name, err)
	}
	return nil
}

// DeleteByName removes the named credential; false when it does not exist.
func (s *Store) DeleteByName(name string) (bool, error) {
	res := s.db.Where("name = ?", name).Delete(&Credential{})
	if res.Error != nil {
		return false, fmt.Errorf("delete credential %s: %w", name, res.Error)
	}
	return res.RowsAffected > 0, nil
}
