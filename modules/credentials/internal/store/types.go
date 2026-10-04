// Package store holds the credentials module's GORM model: one row per
// named credential, its payload encrypted at rest.
package store

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Model carries the columns shared by the module's tables.
type Model struct {
	ID        uint      `json:"id" gorm:"primarykey"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// GitCred authenticates git fetches over http(s) with basic auth. Host is
// an optional identification label: the build path attaches git auth to the
// request's single git source, so it is not matched against today.
type GitCred struct {
	Host     string `json:"host,omitempty"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// RegCred authenticates pushes to one registry host; hosts match by
// longest prefix, aligned with fleet.Creds.Table.
type RegCred struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Payload is the decrypted credential body (docs/demo/workflow-design.md
// §9): exactly the shape the build path consumes.
type Payload struct {
	Git      *GitCred  `json:"git,omitempty"`
	Registry []RegCred `json:"registry,omitempty"`
}

// Credential is one named credential. Payload is write-only: it decrypts
// on scan, so rows must never be serialized to clients or logs.
type Credential struct {
	Model
	Name        string     `json:"name" gorm:"type:varchar(63);uniqueIndex;not null"`
	Description string     `json:"description" gorm:"type:text"`
	Payload     SecretJSON `json:"-" gorm:"type:text"`
}

func (Credential) TableName() string {
	return "credentials"
}

// SecretJSON stores a JSON document encrypted at rest with the host's
// KITE_ENCRYPT_KEY. The module cannot import the host, so encryption is
// provided through the codec wired by SetSecretCodec; the wire format is
// the module-side counterpart of the host's model.SecretString and
// ciphertext is readable by either side.
type SecretJSON []byte

// errSecretCodecNotWired is returned by SecretJSON operations before the
// host wires the codec, instead of silently storing plaintext.
var errSecretCodecNotWired = errors.New("credentials store: secret codec not wired")

// GORM invokes Scan/Value without access to Deps, so the host-provided
// implementations are package-level state wired once via SetSecretCodec.
var (
	encryptFn func(plaintext string) (string, error)
	decryptFn func(encrypted string) (string, error)
)

// SetSecretCodec wires the SecretJSON codec. It must be called before any
// module table is read or written.
func SetSecretCodec(encrypt, decrypt func(string) (string, error)) {
	encryptFn, decryptFn = encrypt, decrypt
}

// Scan implements the sql.Scanner interface.
func (s *SecretJSON) Scan(value interface{}) error {
	if value == nil {
		*s = nil
		return nil
	}
	var encrypted string
	switch v := value.(type) {
	case string:
		encrypted = v
	case []byte:
		encrypted = string(v)
	default:
		return fmt.Errorf("cannot scan %T into SecretJSON", value)
	}
	if encrypted == "" {
		*s = nil
		return nil
	}
	if decryptFn == nil {
		return errSecretCodecNotWired
	}
	plaintext, err := decryptFn(encrypted)
	if err != nil {
		return fmt.Errorf("decrypt SecretJSON: %w", err)
	}
	*s = SecretJSON(plaintext)
	return nil
}

// Value implements the driver.Valuer interface.
func (s SecretJSON) Value() (driver.Value, error) {
	if len(s) == 0 {
		return "", nil
	}
	if encryptFn == nil {
		return nil, errSecretCodecNotWired
	}
	encrypted, err := encryptFn(string(s))
	if err != nil {
		return nil, fmt.Errorf("encrypt SecretJSON: %w", err)
	}
	return encrypted, nil
}

// Unmarshal deserializes the JSON data into the provided interface.
func (s SecretJSON) Unmarshal(v interface{}) error {
	if s == nil {
		return nil
	}
	return json.Unmarshal(s, v)
}

// Marshal serializes the provided interface into JSON.
func (s *SecretJSON) Marshal(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	*s = data
	return nil
}
