package render

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ManagedPullSecretPrefix marks the managed image pull secret names the
// deploy funnel upserts ("kite-pull-"+credential, hash-suffixed when the
// derived name would exceed the DNS-1123 label limit).
const ManagedPullSecretPrefix = "kite-pull-"

// InjectPullSecret makes every workload document reference the managed image
// pull secret via spec.template.spec.imagePullSecrets, so a config with a
// pull credential publishes pods that can actually fetch private images
// without hand-editing manifests. Entries for previously managed secrets
// (the prefix or the exact name) are replaced; foreign entries the user
// added themselves are preserved. An empty secretName is a no-op: clearing
// happens by re-render, which drops the reference wholesale.
func InjectPullSecret(docs []unstructured.Unstructured, secretName string) {
	if secretName == "" {
		return
	}
	for i := range docs {
		if !isWorkloadKind(&docs[i]) {
			continue
		}
		path := []string{"spec", "template", "spec", "imagePullSecrets"}
		cur, _, _ := unstructured.NestedSlice(docs[i].Object, path...)
		next := make([]interface{}, 0, len(cur)+1)
		for _, entry := range cur {
			m, ok := entry.(map[string]interface{})
			if ok && managedPullSecretEntry(m, secretName) {
				continue
			}
			next = append(next, entry)
		}
		next = append(next, map[string]interface{}{"name": secretName})
		_ = unstructured.SetNestedSlice(docs[i].Object, next, path...)
	}
}

func managedPullSecretEntry(entry map[string]interface{}, secretName string) bool {
	name, _ := entry["name"].(string)
	return name == secretName || strings.HasPrefix(name, ManagedPullSecretPrefix)
}

// RemovePullSecret drops the managed image pull secret entries from every
// workload document (the exact name plus anything kite-managed), preserving
// foreign entries. It is the clearing counterpart of InjectPullSecret.
func RemovePullSecret(docs []unstructured.Unstructured, secretName string) {
	for i := range docs {
		if !isWorkloadKind(&docs[i]) {
			continue
		}
		path := []string{"spec", "template", "spec", "imagePullSecrets"}
		cur, _, _ := unstructured.NestedSlice(docs[i].Object, path...)
		next := make([]interface{}, 0, len(cur))
		for _, entry := range cur {
			m, ok := entry.(map[string]interface{})
			if ok && managedPullSecretEntry(m, secretName) {
				continue
			}
			next = append(next, entry)
		}
		if len(next) == 0 {
			unstructured.RemoveNestedField(docs[i].Object, path...)
			continue
		}
		_ = unstructured.SetNestedSlice(docs[i].Object, next, path...)
	}
}
