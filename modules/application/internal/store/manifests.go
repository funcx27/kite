package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"
)

// Identity label trio stamped on every rendered/adopted document (design §5),
// plus the space marker carried by namespaces the module creates.
const (
	LabelAppName   = "app.kubernetes.io/name"
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelKiteName  = "kite.application/name"
	LabelKiteSpace = "kite.application/space"
	// AnnotationKiteManaged marks resources kite maintains outside the
	// manifest sets (the per-config image pull secret): no identity label on
	// purpose, so sync adoption and the anchor-based drift never see it.
	AnnotationKiteManaged = "kite.application/managed"
	// AnnotationKiteCertificate names the platform certificate an Ingress's
	// TLS block was rendered from; the publish pipeline resolves it to the
	// managed TLS secret and status checks use it for rotation visibility.
	AnnotationKiteCertificate = "kite.application/certificate"
	// AnnotationKiteCertHash carries the certificate content hash the managed
	// TLS secret was last written with; a mismatch means the certificate was
	// rotated and the config awaits a publish.
	AnnotationKiteCertHash = "kite.application/cert-hash"
)

// AsMap returns v as a JSON object tree node.
func AsMap(v interface{}) (map[string]interface{}, bool) {
	m, ok := v.(map[string]interface{})
	return m, ok
}

// AsList returns v as a JSON array tree node.
func AsList(v interface{}) ([]interface{}, bool) {
	l, ok := v.([]interface{})
	return l, ok
}

// DocKey identifies a document across manifest sets: (kind, namespace, name)
// (design §4.5).
func DocKey(kind, namespace, name string) string {
	return kind + "/" + namespace + "/" + name
}

func DocKeyOf(doc *unstructured.Unstructured) string {
	return DocKey(doc.GetKind(), doc.GetNamespace(), doc.GetName())
}

// DecodeManifests parses the stored JSON array of documents. Numbers are
// canonicalized so values decoded from different sources (DB, requests,
// ExtraYAML, cluster read-back) compare equal in diff.
func DecodeManifests(data []byte) ([]unstructured.Unstructured, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw []interface{}
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode manifests: %w", err)
	}
	docs := make([]unstructured.Unstructured, 0, len(raw))
	for i, item := range raw {
		obj, ok := canonicalizeNumbers(item).(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("decode manifests: document %d is not an object", i)
		}
		docs = append(docs, unstructured.Unstructured{Object: obj})
	}
	return docs, nil
}

// EncodeManifests serializes the documents back to the stored JSON form. The
// []byte result is assignable to the module's JSONField / SecretJSON columns.
func EncodeManifests(docs []unstructured.Unstructured) ([]byte, error) {
	data, err := json.Marshal(docs)
	if err != nil {
		return nil, fmt.Errorf("encode manifests: %w", err)
	}
	return data, nil
}

// ManifestsToYAML renders the documents as a multi-document YAML string.
func ManifestsToYAML(docs []unstructured.Unstructured) (string, error) {
	var b strings.Builder
	for i := range docs {
		if i > 0 {
			b.WriteString("---\n")
		}
		chunk, err := sigsyaml.Marshal(docs[i].Object)
		if err != nil {
			return "", fmt.Errorf("encode yaml: %w", err)
		}
		b.Write(chunk)
	}
	return b.String(), nil
}

// ParseExtraYAML splits the ExtraYAML multi-document stream into documents
// (design §9). Empty documents are skipped.
func ParseExtraYAML(s string) ([]unstructured.Unstructured, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var docs []unstructured.Unstructured
	for _, chunk := range splitYAMLDocuments(s) {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		var obj map[string]interface{}
		if err := sigsyaml.Unmarshal([]byte(chunk), &obj); err != nil {
			return nil, fmt.Errorf("decode document: %w", err)
		}
		if len(obj) == 0 {
			continue
		}
		docs = append(docs, unstructured.Unstructured{Object: canonicalizeNumbers(obj).(map[string]interface{})})
	}
	return docs, nil
}

// splitYAMLDocuments splits a multi-document YAML stream at column-0 "---"
// marker lines; block scalar content is always indented, so it never ends
// with a marker.
func splitYAMLDocuments(s string) []string {
	lines := strings.Split(s, "\n")
	docs := make([]string, 0, 2)
	var cur []string
	for _, line := range lines {
		if strings.TrimRight(line, " \t") == "---" {
			docs = append(docs, strings.Join(cur, "\n"))
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	return append(docs, strings.Join(cur, "\n"))
}

// canonicalizeNumbers rewrites every number as int64 when integral, keeping
// only genuinely fractional values as float64, so trees built from different
// decoders are deep-equal where they hold the same values.
func canonicalizeNumbers(v interface{}) interface{} {
	switch t := v.(type) {
	case json.Number:
		if i, err := strconv.ParseInt(string(t), 10, 64); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1<<62 {
			return int64(t)
		}
		return t
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case map[string]interface{}:
		for k, child := range t {
			t[k] = canonicalizeNumbers(child)
		}
		return t
	case []interface{}:
		for i, child := range t {
			t[i] = canonicalizeNumbers(child)
		}
		return t
	default:
		return v
	}
}
