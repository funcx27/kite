package store

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// HistoryEntry is one audit record for a state change performed by the
// module. The host persists it as a ResourceHistory row attributed to the
// application module.
type HistoryEntry struct {
	Cluster      string
	ResourceType string
	ResourceName string
	Namespace    string
	Operation    string
	YAML         string
	PreviousYAML string
	Success      bool
	Err          string
	OperatorID   uint
}

// JSONField stores arbitrary JSON documents as raw bytes.
type JSONField []byte

// Scan implements the sql.Scanner interface.
func (j *JSONField) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}
	switch v := value.(type) {
	case string:
		*j = []byte(v)
	case []byte:
		*j = v
	default:
		return fmt.Errorf("cannot scan %T into JSONField", value)
	}
	return nil
}

// Value implements the driver.Valuer interface.
func (j JSONField) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return string(j), nil
}

// Unmarshal deserializes the JSON data into the provided interface.
func (j JSONField) Unmarshal(v interface{}) error {
	if j == nil {
		return nil
	}
	return json.Unmarshal(j, v)
}

// Marshal serializes the provided interface into JSON.
func (j *JSONField) Marshal(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	*j = data
	return nil
}

// SecretJSON stores a JSON document encrypted at rest with the host's
// KITE_ENCRYPT_KEY. The module cannot import the host, so encryption is
// provided through the codec wired by SetSecretCodec; the wire format is the
// module-side counterpart of the host's model.SecretString and ciphertext is
// readable by either side. Used where snapshots carry Secret values
// (ConfigRevision.Manifests); display-side masking is handled when serving.
type SecretJSON []byte

// errSecretCodecNotWired is returned by SecretJSON operations before the
// host wires the codec, instead of silently storing plaintext.
var errSecretCodecNotWired = errors.New("application store: secret codec not wired")

// GORM invokes Scan/Value without access to Deps, so the host-provided
// implementations are package-level state wired once at adapter construction.
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
