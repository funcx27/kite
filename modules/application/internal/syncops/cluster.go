package syncops

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/application/internal/store"
)

var kindAPIVersions = map[string]string{
	"Deployment":  "apps/v1",
	"StatefulSet": "apps/v1",
	"Service":     "v1",
	"Ingress":     "networking.k8s.io/v1",
	"ConfigMap":   "v1",
	"Secret":      "v1",
}

// parseGVK derives the GroupVersionKind of a decoded document.
func parseGVK(doc *unstructured.Unstructured) (schema.GroupVersionKind, error) {
	if doc.GetKind() == "" {
		return schema.GroupVersionKind{}, fmt.Errorf("document %s has no kind", store.DocKeyOf(doc))
	}
	gv, err := schema.ParseGroupVersion(doc.GetAPIVersion())
	if err != nil {
		return schema.GroupVersionKind{}, fmt.Errorf("parse apiVersion %q: %w", doc.GetAPIVersion(), err)
	}
	return gv.WithKind(doc.GetKind()), nil
}

// RestMapping resolves the API resource (plural name and scope) of a document
// through the cluster's RESTMapper (design §6: 逐文档 RESTMapper 判 scope).
func RestMapping(cli client.Client, doc *unstructured.Unstructured) (meta.RESTMapping, error) {
	gvk, err := parseGVK(doc)
	if err != nil {
		return meta.RESTMapping{}, err
	}
	mapping, err := cli.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return meta.RESTMapping{}, fmt.Errorf("resolve api resource of %v: %w", gvk, err)
	}
	return *mapping, nil
}

// BlankDoc returns an empty unstructured object of the document's GVK, ready
// for Get/Delete calls.
func BlankDoc(tpl *unstructured.Unstructured) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(tpl.GetAPIVersion())
	obj.SetKind(tpl.GetKind())
	return obj
}

// IdentityTemplates returns one template document per (kind, namespace, name)
// across manifests and anchor, manifests order first: the fetch/apply set.
func IdentityTemplates(manifests, anchor []unstructured.Unstructured) []unstructured.Unstructured {
	seen := map[string]bool{}
	var out []unstructured.Unstructured
	add := func(docs []unstructured.Unstructured) {
		for i := range docs {
			key := store.DocKeyOf(&docs[i])
			if seen[key] {
				continue
			}
			seen[key] = true
			tpl := unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": docs[i].GetAPIVersion(),
				"kind":       docs[i].GetKind(),
			}}
			tpl.SetName(docs[i].GetName())
			tpl.SetNamespace(docs[i].GetNamespace())
			out = append(out, tpl)
		}
	}
	add(manifests)
	add(anchor)
	return out
}

// FetchLiveDocs reads the cluster state of the identity documents; objects
// absent on the cluster are simply missing from the result, other read errors
// abort.
func FetchLiveDocs(ctx context.Context, cli client.Client, templates []unstructured.Unstructured) (map[string]*unstructured.Unstructured, error) {
	live := make(map[string]*unstructured.Unstructured, len(templates))
	for i := range templates {
		tpl := &templates[i]
		obj := BlankDoc(tpl)
		err := cli.Get(ctx, client.ObjectKey{Namespace: tpl.GetNamespace(), Name: tpl.GetName()}, obj)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get %s: %w", store.DocKeyOf(tpl), err)
		}
		live[store.DocKeyOf(tpl)] = obj
	}
	return live, nil
}

// ListManagedByLabel lists the managed-kind documents carrying the app's
// identity label in the config namespace (design §6 sync).
func ListManagedByLabel(ctx context.Context, cli client.Client, app, namespace string) (map[string]*unstructured.Unstructured, error) {
	out := map[string]*unstructured.Unstructured{}
	for kind, apiVersion := range kindAPIVersions {
		list := &unstructured.UnstructuredList{}
		list.SetAPIVersion(apiVersion)
		list.SetKind(kind + "List")
		err := cli.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels{store.LabelKiteName: app})
		if err != nil {
			return nil, fmt.Errorf("list %s by label: %w", kind, err)
		}
		for i := range list.Items {
			out[store.DocKeyOf(&list.Items[i])] = &list.Items[i]
		}
	}
	return out, nil
}

// ScopedPath joins a document key and a document-relative path into the one
// key format used by OverriddenPaths and the merge request ("docKey never
// contains a colon").
func ScopedPath(docKey, path string) string { return docKey + ":" + path }

func SplitScopedPath(s string) (string, string) {
	if i := strings.Index(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, DocRootPath
}

func findDocByKey(docs []unstructured.Unstructured, key string) *unstructured.Unstructured {
	for i := range docs {
		if store.DocKeyOf(&docs[i]) == key {
			return &docs[i]
		}
	}
	return nil
}

// splitSelector splits a trailing "[N]" / "[name=x]" off a path token.
func splitSelector(token string) (key string, idx int, name string, has bool) {
	i := strings.Index(token, "[")
	if i < 0 || !strings.HasSuffix(token, "]") {
		return token, -1, "", false
	}
	key = token[:i]
	inner := token[i+1 : len(token)-1]
	if strings.HasPrefix(inner, "name=") {
		return key, -1, strings.TrimPrefix(inner, "name="), true
	}
	n, err := strconv.Atoi(inner)
	if err != nil || n < 0 {
		return token, -1, "", false
	}
	return key, n, "", true
}

// resolvePathValue resolves a diff/merge leaf path ("spec.replicas",
// "spec.template.spec.containers[0].image",
// "spec.template.spec.containers[name=app].env", or DocRootPath) against a
// document tree. Dotted map keys (annotation keys) are resolved greedily.
func resolvePathValue(root interface{}, path string) (interface{}, bool) {
	if path == DocRootPath {
		return root, true
	}
	tokens := strings.Split(path, ".")
	var cur interface{} = root
	for i := 0; i < len(tokens); {
		m, ok := store.AsMap(cur)
		if !ok {
			return nil, false
		}
		key, idx, name, hasSelector := splitSelector(tokens[i])
		if hasSelector {
			v, exists := m[key]
			if !exists {
				return nil, false
			}
			list, ok := store.AsList(v)
			if !ok {
				return nil, false
			}
			if name != "" {
				found := false
				for _, e := range list {
					if em, ok := store.AsMap(e); ok && em["name"] == name {
						cur, found = em, true
						break
					}
				}
				if !found {
					return nil, false
				}
			} else {
				if idx >= len(list) {
					return nil, false
				}
				cur = list[idx]
			}
			i++
			continue
		}
		if _, exists := m[key]; !exists {
			// Greedy longest dotted key: annotation keys may contain dots.
			for j := i + 1; j < len(tokens); j++ {
				cand := strings.Join(tokens[i:j+1], ".")
				if _, exists := m[cand]; exists {
					key, i = cand, j
					break
				}
			}
		}
		v, exists := m[key]
		if !exists {
			return nil, false
		}
		cur, i = v, i+1
	}
	return cur, true
}

// liveMatchesManifests reports whether the live object already carries the
// manifests value at path — the merged-adopted case of the conflict gate
// (design §6).
func liveMatchesManifests(manifests []unstructured.Unstructured, live *unstructured.Unstructured, path string) bool {
	md := findDocByKey(manifests, store.DocKeyOf(live))
	if md == nil || live == nil {
		return false
	}
	av, okA := resolvePathValue(md.Object, path)
	bv, okB := resolvePathValue(live.Object, path)
	if !okA || !okB {
		return okA == okB
	}
	return deepEqualJSON(av, bv)
}

// overrideMatches reports whether a scoped override covers the drift path.
// Diff paths carry [N] list selectors while merge-reported overrides
// (choices kept on the DB side, rollbacks) carry [name=x]; both spellings
// are matched against the live object's element names.
func overrideMatches(overridden map[string]bool, live *unstructured.Unstructured, scoped string) bool {
	if overridden[scoped] {
		return true
	}
	docKey, path := SplitScopedPath(scoped)
	if nameKeyed, ok := nameKeyedPath(live.Object, path); ok && nameKeyed != path {
		return overridden[ScopedPath(docKey, nameKeyed)]
	}
	return false
}

// nameKeyedPath rewrites every [N] list selector of the path to [name=x]
// using the element names of the given object; it fails when any list
// element is missing or unnamed.
func nameKeyedPath(root interface{}, path string) (string, bool) {
	if path == DocRootPath {
		return path, true
	}
	tokens := strings.Split(path, ".")
	var cur interface{} = root
	nameKeyed := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		key, idx, name, hasSelector := splitSelector(tok)
		m, ok := store.AsMap(cur)
		if !ok {
			return "", false
		}
		v, exists := m[key]
		if !exists {
			return "", false
		}
		if !hasSelector {
			cur = v
			nameKeyed = append(nameKeyed, tok)
			continue
		}
		list, ok := store.AsList(v)
		if !ok {
			return "", false
		}
		var elem map[string]interface{}
		switch {
		case name != "":
			for _, e := range list {
				if em, ok := store.AsMap(e); ok && em["name"] == name {
					elem = em
					break
				}
			}
		case idx >= 0 && idx < len(list):
			elem, _ = store.AsMap(list[idx])
		}
		if elem == nil {
			return "", false
		}
		if n, _ := elem["name"].(string); n != "" {
			nameKeyed = append(nameKeyed, key+"[name="+n+"]")
		} else {
			nameKeyed = append(nameKeyed, tok)
		}
		cur = elem
	}
	return strings.Join(nameKeyed, "."), true
}

// LiveList projects the live map onto the ordered doc set.
func LiveList(live map[string]*unstructured.Unstructured, order []unstructured.Unstructured) []unstructured.Unstructured {
	out := make([]unstructured.Unstructured, 0, len(live))
	for i := range order {
		if d, ok := live[store.DocKeyOf(&order[i])]; ok {
			out = append(out, *d)
		}
	}
	return out
}

// ConflictDetail is one document's unresolved cluster-side drift paths
// reported by the publish conflict gate.
type ConflictDetail struct {
	Kind      string   `json:"kind"`
	Namespace string   `json:"namespace,omitempty"`
	Name      string   `json:"name"`
	Paths     []string `json:"paths"`
}

// ConflictGate implements the publish conflict gate (design §6): every
// cluster-side drift path of a document the manifests still contain must
// either match the manifests value (merged adoption) or be listed in
// OverriddenPaths (knowing override), otherwise it is reported as a conflict.
func ConflictGate(manifests, anchor []unstructured.Unstructured, live map[string]*unstructured.Unstructured, overridden map[string]bool) []ConflictDetail {
	manifestKeys := map[string]bool{}
	for i := range manifests {
		manifestKeys[store.DocKeyOf(&manifests[i])] = true
	}
	var out []ConflictDetail
	for i := range anchor {
		ad := &anchor[i]
		key := store.DocKeyOf(ad)
		if !manifestKeys[key] {
			// Document dropped from the manifests: publishing no longer
			// touches it, so its drift cannot be overwritten.
			continue
		}
		liveDoc := live[key]
		if liveDoc == nil {
			// Deleted cluster-side while the manifests still want it.
			if !overridden[ScopedPath(key, DocRootPath)] {
				out = append(out, ConflictDetail{Kind: ad.GetKind(), Namespace: ad.GetNamespace(), Name: ad.GetName(), Paths: []string{DocRootPath}})
			}
			continue
		}
		var unresolved []string
		// Complete drift set: the gate must cover every cluster-side change,
		// not just the capped display diff.
		for _, p := range docDiffPaths(liveDoc, ad) {
			switch {
			case overrideMatches(overridden, liveDoc, ScopedPath(key, p)):
			case liveMatchesManifests(manifests, liveDoc, p):
			default:
				unresolved = append(unresolved, p)
			}
		}
		if len(unresolved) > 0 {
			out = append(out, ConflictDetail{Kind: ad.GetKind(), Namespace: ad.GetNamespace(), Name: ad.GetName(), Paths: unresolved})
		}
	}
	return out
}
