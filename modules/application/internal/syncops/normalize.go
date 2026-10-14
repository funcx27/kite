package syncops

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/funcx27/kite/modules/application/internal/store"
)

// MaxDiffPaths caps the collected leaf paths per document (design §4.6).
const MaxDiffPaths = 20

// curatedAnnotations are controller/CLI-managed annotations stripped by
// Normalize so they never surface as drift (design §4.3). restartedAt is
// included because kite's own restart bumps the pod template.
var curatedAnnotations = map[string]struct{}{
	"deployment.kubernetes.io/revision":                {},
	"deployment.kubernetes.io/desired-replicas":        {},
	"deployment.kubernetes.io/max-replicas":            {},
	"kubectl.kubernetes.io/last-applied-configuration": {},
	"kubectl.kubernetes.io/restartedAt":                {},
	"kite.kubernetes.io/restartedAt":                   {},
}

// metadataFields are server-managed metadata fields stripped by Normalize.
var metadataFields = []string{"resourceVersion", "uid", "generation", "creationTimestamp", "selfLink", "managedFields"}

// specServerFields are spec fields the API server allocates or defaults and
// the rendered manifests never set (design §4.1: read-back anchoring absorbs
// server defaulting so a fresh publish reports no fake drift). Names are
// chosen to only ever appear on kinds whose defaulting they reflect.
var specServerFields = []string{
	"clusterIP", "clusterIPs", "internalTrafficPolicy", "sessionAffinity",
	"ipFamilies", "ipFamilyPolicy",
	"strategy", "revisionHistoryLimit", "progressDeadlineSeconds",
	"podManagementPolicy", "updateStrategy", "serviceName",
	"persistentVolumeClaimRetentionPolicy",
}

// podSpecServerFields are pod-spec fields the API server always fills in.
var podSpecServerFields = []string{"dnsPolicy", "restartPolicy", "schedulerName", "terminationGracePeriodSeconds"}

// containerServerFields are per-container fields the API server always fills
// in and the rendered manifests never own. imagePullPolicy is not among
// them: the wizard renders it from the spec, so it is user-owned and must
// diff — the server defaulting of an unset policy is absorbed cross-side by
// AbsorbDefaultedPullPolicy instead.
var containerServerFields = []string{"terminationMessagePath", "terminationMessagePolicy"}

// deleteIfEmptyMap removes key when its value is an empty JSON object — a
// server-added placeholder such as securityContext: {}. A user-set value stays.
func deleteIfEmptyMap(m map[string]interface{}, key string) {
	if v, ok := m[key].(map[string]interface{}); ok && len(v) == 0 {
		delete(m, key)
	}
}

// Normalize returns a deep copy of obj with server-managed fields removed:
// volatile metadata, the top-level status subtree, Service cluster IP / node
// port assignments, defaulted spec/pod-template fields and curated controller
// annotations (design §4.3). Both diff sides go through the same function.
func Normalize(obj *unstructured.Unstructured) *unstructured.Unstructured {
	out := obj.DeepCopy()
	normalizeValue(out.Object, true)
	foldSecretStringData(out)
	return out
}

// foldSecretStringData merges stringData into data (base64) the way the API
// server does on write and drops stringData, so a manifest authored with
// stringData compares equal to its live object. Plaintext that is not valid
// UTF-8 handled by the writer never reaches the stored manifests.
func foldSecretStringData(obj *unstructured.Unstructured) {
	if obj.GetKind() != "Secret" {
		return
	}
	raw, ok := store.AsMap(obj.Object["stringData"])
	if !ok {
		return
	}
	data, _ := store.AsMap(obj.Object["data"])
	if data == nil {
		data = map[string]interface{}{}
	}
	for k, v := range raw {
		if s, ok := v.(string); ok {
			data[k] = base64.StdEncoding.EncodeToString([]byte(s))
		}
	}
	obj.Object["data"] = data
	delete(obj.Object, "stringData")
}

// NormalizeManifests normalizes every document of the set.
func NormalizeManifests(docs []unstructured.Unstructured) []unstructured.Unstructured {
	out := make([]unstructured.Unstructured, len(docs))
	for i := range docs {
		out[i] = *Normalize(&docs[i])
	}
	return out
}

func normalizeValue(v interface{}, top bool) {
	m, ok := v.(map[string]interface{})
	if !ok {
		return
	}
	if top {
		delete(m, "status")
	}
	if meta, ok := m["metadata"].(map[string]interface{}); ok {
		for _, f := range metadataFields {
			delete(meta, f)
		}
		if ann, ok := meta["annotations"].(map[string]interface{}); ok {
			for k := range ann {
				if _, hit := curatedAnnotations[k]; hit {
					delete(ann, k)
				}
			}
			// An emptied map would itself surface as a diff path.
			if len(ann) == 0 {
				delete(meta, "annotations")
			}
		}
	}
	if spec, ok := m["spec"].(map[string]interface{}); ok {
		for _, f := range specServerFields {
			delete(spec, f)
		}
		if ports, ok := spec["ports"].([]interface{}); ok {
			for _, p := range ports {
				if pm, ok := p.(map[string]interface{}); ok {
					delete(pm, "nodePort")
				}
			}
		}
		if tpl, ok := spec["template"].(map[string]interface{}); ok {
			if podSpec, ok := tpl["spec"].(map[string]interface{}); ok {
				for _, f := range podSpecServerFields {
					delete(podSpec, f)
				}
				deleteIfEmptyMap(podSpec, "securityContext")
				for _, key := range []string{"containers", "initContainers"} {
					containers, ok := podSpec[key].([]interface{})
					if !ok {
						continue
					}
					for _, c := range containers {
						cm, ok := c.(map[string]interface{})
						if !ok {
							continue
						}
						for _, f := range containerServerFields {
							delete(cm, f)
						}
						deleteIfEmptyMap(cm, "resources")
					}
				}
			}
		}
	}
	for _, child := range m {
		normalizeValue(child, false)
	}
}

// AbsorbDefaultedPullPolicy removes the API server's defaulted
// imagePullPolicy from live's containers whose manifest counterpart does not
// set the field: the server fills the policy on every write, so an unset
// policy must not read the defaulted value back as drift. A manifest that
// sets the policy owns it and the field is compared normally (design §4.1).
func AbsorbDefaultedPullPolicy(manifest, live *unstructured.Unstructured) {
	if manifest == nil || live == nil {
		return
	}
	for _, key := range []string{"containers", "initContainers"} {
		mcs := templateContainers(manifest, key)
		if len(mcs) == 0 {
			continue
		}
		lcs := templateContainers(live, key)
		for name, mc := range mcs {
			if _, set := mc["imagePullPolicy"]; set {
				continue
			}
			if lc := lcs[name]; lc != nil {
				delete(lc, "imagePullPolicy")
			}
		}
	}
}

// templateContainers indexes a pod-template container list by container name.
// It walks the raw maps on purpose: unstructured.NestedSlice deep-copies its
// result, which would disconnect the deletion in AbsorbDefaultedPullPolicy
// from the document it must modify.
func templateContainers(doc *unstructured.Unstructured, key string) map[string]map[string]interface{} {
	spec, _ := store.AsMap(doc.Object["spec"])
	if spec == nil {
		return nil
	}
	tpl, _ := store.AsMap(spec["template"])
	if tpl == nil {
		return nil
	}
	podSpec, _ := store.AsMap(tpl["spec"])
	if podSpec == nil {
		return nil
	}
	list, _ := store.AsList(podSpec[key])
	out := make(map[string]map[string]interface{}, len(list))
	for _, e := range list {
		m, ok := store.AsMap(e)
		if !ok {
			continue
		}
		if n, _ := m["name"].(string); n != "" {
			out[n] = m
		}
	}
	return out
}

// DocDiff is the per-document change set of DiffManifests.
type DocDiff struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// MissingIn is "manifests" or "cluster" when the document exists on one
	// side only: the side it is missing from.
	MissingIn string   `json:"missingIn,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// DiffManifests aligns the manifest set and the live set by (kind, namespace,
// name) after normalization and collects the changed leaf paths per document
// (design §4.4–§4.6). MissingIn names the side a document is missing from.
// Paths use JSON notation with array indices, e.g.
// spec.template.spec.containers[0].image. A key present on only one side
// counts as a change at that path.
func DiffManifests(manifests, live []unstructured.Unstructured) []DocDiff {
	nm, nl := NormalizeManifests(manifests), NormalizeManifests(live)
	liveIdx := make(map[string]int, len(nl))
	for i := range nl {
		liveIdx[store.DocKeyOf(&nl[i])] = i
	}
	seen := make(map[string]bool, len(nm))
	out := make([]DocDiff, 0, len(nm)+len(nl))
	for i := range nm {
		key := store.DocKeyOf(&nm[i])
		seen[key] = true
		d := DocDiff{Kind: nm[i].GetKind(), Namespace: nm[i].GetNamespace(), Name: nm[i].GetName()}
		j, ok := liveIdx[key]
		if !ok {
			d.MissingIn = "cluster"
			out = append(out, d)
			continue
		}
		AbsorbDefaultedPullPolicy(&nm[i], &nl[j])
		sink := &diffSink{limit: MaxDiffPaths}
		diffValue(nm[i].Object, nl[j].Object, "", sink)
		if len(sink.paths) == 0 {
			continue
		}
		d.Paths, d.Truncated = sink.paths, sink.truncated
		out = append(out, d)
	}
	for i := range nl {
		key := store.DocKeyOf(&nl[i])
		if seen[key] {
			continue
		}
		out = append(out, DocDiff{
			Kind: nl[i].GetKind(), Namespace: nl[i].GetNamespace(), Name: nl[i].GetName(),
			MissingIn: "manifests",
		})
	}
	return out
}

// absent marks a key that does not exist on one side; distinct from a JSON
// null value, which compares like any other leaf.
type absentType struct{}

var absent = absentType{}

func deepEqualJSON(a, b interface{}) bool {
	return reflect.DeepEqual(a, b)
}

func pick(m map[string]interface{}, k string) interface{} {
	if v, ok := m[k]; ok {
		return v
	}
	return absent
}

func sortedUnionKeys(maps ...map[string]interface{}) []string {
	seen := map[string]bool{}
	for _, m := range maps {
		for k := range m {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type diffSink struct {
	paths     []string
	truncated bool
	limit     int // 0 = unbounded
}

func (s *diffSink) add(path string) {
	if s.limit > 0 && len(s.paths) >= s.limit {
		s.truncated = true
		return
	}
	s.paths = append(s.paths, path)
}

// diffValue walks two trees and records the leaf paths where they differ.
// Maps recurse per key; lists whose elements all carry a non-empty name
// string align by name (paths carry a [name=x] selector, a single-side name
// is one change at that path), other lists per index (an insertion shifts
// indices and is reported at each affected path, capped by MaxDiffPaths);
// any other combination is a leaf.
func diffValue(a, b interface{}, path string, sink *diffSink) {
	if sink.truncated || deepEqualJSON(a, b) {
		return
	}
	am, aok := a.(map[string]interface{})
	bm, bok := b.(map[string]interface{})
	if aok && bok {
		for _, k := range sortedUnionKeys(am, bm) {
			if sink.truncated {
				return
			}
			child := k
			if path != "" {
				child = path + "." + k
			}
			diffValue(pick(am, k), pick(bm, k), child, sink)
		}
		return
	}
	al, aok := a.([]interface{})
	bl, bok := b.([]interface{})
	if aok && bok {
		if pairs, ok := alignListByName(al, bl); ok {
			for _, p := range pairs {
				if sink.truncated {
					return
				}
				diffValue(p.av, p.bv, fmt.Sprintf("%s[name=%s]", path, p.name), sink)
			}
			return
		}
		n := len(al)
		if len(bl) > n {
			n = len(bl)
		}
		for i := 0; i < n; i++ {
			if sink.truncated {
				return
			}
			av, bv := interface{}(absent), interface{}(absent)
			if i < len(al) {
				av = al[i]
			}
			if i < len(bl) {
				bv = bl[i]
			}
			diffValue(av, bv, fmt.Sprintf("%s[%d]", path, i), sink)
		}
		return
	}
	sink.add(path)
}

// namedElem is one name-aligned pair of list elements; a missing side holds
// the absent marker.
type namedElem struct {
	name   string
	av, bv interface{}
}

// alignListByName pairs two lists' elements by their name key, names in
// sorted union order so the diff is deterministic. It fails when any element
// is not a map with a non-empty name string, falling back to index alignment.
func alignListByName(al, bl []interface{}) ([]namedElem, bool) {
	aIdx := elementsByName(al)
	if aIdx == nil {
		return nil, false
	}
	bIdx := elementsByName(bl)
	if bIdx == nil {
		return nil, false
	}
	seen := map[string]bool{}
	var names []string
	for n := range aIdx {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for n := range bIdx {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := make([]namedElem, 0, len(names))
	for _, n := range names {
		av, bv := interface{}(absent), interface{}(absent)
		if v, ok := aIdx[n]; ok {
			av = v
		}
		if v, ok := bIdx[n]; ok {
			bv = v
		}
		out = append(out, namedElem{name: n, av: av, bv: bv})
	}
	return out, true
}

// elementsByName indexes a list's map elements by their name string; nil when
// any element is not a map or carries no non-empty name.
func elementsByName(list []interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(list))
	for _, e := range list {
		m, ok := store.AsMap(e)
		if !ok {
			return nil
		}
		n, _ := m["name"].(string)
		if n == "" {
			return nil
		}
		out[n] = e
	}
	return out
}
