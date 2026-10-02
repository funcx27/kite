package store

import "gorm.io/gorm"

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
