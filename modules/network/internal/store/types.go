package store

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// Route is one host routing rule; the target namespace derives from the host
// row, so it is not stored per route. Annotations are service-level catalog
// annotations rendered onto the object that carries the route; within one
// appName the set is normalized to be identical on every route.
type Route struct {
	Path        string            `json:"path"`
	AppName     string            `json:"appName"`
	Port        int32             `json:"port"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// HistoryEntry is one audit record for a state change performed by the
// module. The host persists it as a ResourceHistory row attributed to the
// network module.
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
