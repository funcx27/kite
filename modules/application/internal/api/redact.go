package api

import (
	"bytes"
	"encoding/json"
)

// redactMarker replaces sensitive values in audit payloads.
const redactMarker = "***"

// redactJSON masks sensitive values in a JSON document before it is persisted
// to the audit stream (design §9: audit redacts Secret data/stringData to
// "***"): values of k8s Secret data/stringData maps and of wizard config
// files flagged secret. The input must be valid JSON; on any internal error
// it fails closed to a placeholder instead of leaking plaintext. Empty input
// round-trips as JSON null.
func redactJSON(doc []byte) []byte {
	if len(bytes.TrimSpace(doc)) == 0 {
		return []byte("null")
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var tree interface{}
	if err := dec.Decode(&tree); err != nil {
		return []byte(`"<redacted>"`)
	}
	redactNode(tree)
	out, err := json.Marshal(tree)
	if err != nil {
		return []byte(`"<redacted>"`)
	}
	return out
}

func redacted(doc []byte) string {
	if len(doc) == 0 {
		return ""
	}
	return string(redactJSON(doc))
}

func redactNode(node interface{}) {
	switch v := node.(type) {
	case map[string]interface{}:
		if kind, _ := v["kind"].(string); kind == "Secret" {
			redactMapValues(v, "data")
			redactMapValues(v, "stringData")
		}
		if secret, _ := v["secret"].(bool); secret {
			if _, ok := v["content"]; ok {
				v["content"] = redactMarker
			}
		}
		for _, child := range v {
			redactNode(child)
		}
	case []interface{}:
		for _, child := range v {
			redactNode(child)
		}
	}
}

func redactMapValues(obj map[string]interface{}, key string) {
	if m, ok := obj[key].(map[string]interface{}); ok {
		for k := range m {
			m[k] = redactMarker
		}
	}
}
