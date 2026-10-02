package store

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// HistoryEntry is one audit record for a state change performed by the
// module. The host persists it as a ResourceHistory row attributed to the
// workflow module (OperationSource "workflow").
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

// SecretString stores a short secret encrypted at rest with the host's
// KITE_ENCRYPT_KEY (the module-side counterpart of the host's
// model.SecretString; same wire format, readable by either side). Used for
// Workflow.WebhookToken (design §3).
type SecretString string

// errSecretCodecNotWired is returned by SecretString operations before the
// host wires the codec, instead of silently storing plaintext.
var errSecretCodecNotWired = errors.New("workflow store: secret codec not wired")

// GORM invokes Scan/Value without access to Deps, so the host-provided
// implementations are package-level state wired once via SetSecretCodec.
var (
	encryptFn func(plaintext string) (string, error)
	decryptFn func(encrypted string) (string, error)
)

// SetSecretCodec wires the SecretString codec. It must be called before any
// module table is read or written.
func SetSecretCodec(encrypt, decrypt func(string) (string, error)) {
	encryptFn, decryptFn = encrypt, decrypt
}

// Scan implements the sql.Scanner interface.
func (s *SecretString) Scan(value interface{}) error {
	if value == nil {
		*s = ""
		return nil
	}
	var encrypted string
	switch v := value.(type) {
	case string:
		encrypted = v
	case []byte:
		encrypted = string(v)
	default:
		return fmt.Errorf("cannot scan %T into SecretString", value)
	}
	if encrypted == "" {
		*s = ""
		return nil
	}
	if decryptFn == nil {
		return errSecretCodecNotWired
	}
	plaintext, err := decryptFn(encrypted)
	if err != nil {
		return fmt.Errorf("decrypt SecretString: %w", err)
	}
	*s = SecretString(plaintext)
	return nil
}

// Value implements the driver.Valuer interface.
func (s SecretString) Value() (driver.Value, error) {
	if s == "" {
		return "", nil
	}
	if encryptFn == nil {
		return nil, errSecretCodecNotWired
	}
	encrypted, err := encryptFn(string(s))
	if err != nil {
		return nil, fmt.Errorf("encrypt SecretString: %w", err)
	}
	return encrypted, nil
}
