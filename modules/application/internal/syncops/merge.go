package syncops

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/funcx27/kite/modules/application/internal/store"
)

// Collision resolution values of DocResolutions.Choices.
const (
	ChooseOurs   = "ours"   // keep the DB-side (Manifests) value
	ChooseTheirs = "theirs" // adopt the cluster-side (Live) value
)

// DocRootPath marks document-level operations in merge reports and
// resolutions; paths inside a document are JSON leaf paths (design §4).
const DocRootPath = "$doc"

// DocResolutions resolves one document's merge (design §4): Choices picks a
// side per collision path, Rollbacks reverts auto-adopted cluster changes
// back to the baseline (the knowing overrides recorded in OverriddenPaths).
type DocResolutions struct {
	Choices   map[string]string `json:"choices,omitempty"`
	Rollbacks []string          `json:"rollbacks,omitempty"`
}

// MergeResolutions keys documents by their docKey.
type MergeResolutions map[string]DocResolutions

// MergeCollision is a field both sides changed differently. Chosen is empty
// when the merge ran without a resolution; the DB-side value is then kept as
// a placeholder and the caller must collect explicit choices before writing.
type MergeCollision struct {
	Path   string      `json:"path"`
	Base   interface{} `json:"base,omitempty"`
	Ours   interface{} `json:"ours,omitempty"`
	Theirs interface{} `json:"theirs,omitempty"`
	Chosen string      `json:"chosen,omitempty"`
}

// MergeAutoItem is one auto-adopted path together with the baseline value it
// replaced and the adopted cluster value, so the merge preview can show what
// the cluster side actually changed.
type MergeAutoItem struct {
	Path   string      `json:"path"`
	Base   interface{} `json:"base,omitempty"`
	Theirs interface{} `json:"theirs,omitempty"`
}

// MergeDocReport describes what the merge did to one document.
type MergeDocReport struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// OnlyIn is "ours" or "theirs" when the document exists on one side only.
	OnlyIn string `json:"onlyIn,omitempty"`
	// Dropped marks a document present only in the baseline: deleted on both
	// sides, the merge removes it.
	Dropped bool `json:"dropped,omitempty"`
	// AutoAdopted lists the paths taken from the cluster side, DocRootPath
	// for document-level adoptions (cluster additions and deletions).
	AutoAdopted []MergeAutoItem `json:"autoAdopted,omitempty"`
	// Overridden lists auto-adopted paths rolled back to the baseline by the
	// resolutions; the caller persists them as OverriddenPaths (design §4).
	Overridden []string         `json:"overridden,omitempty"`
	Collisions []MergeCollision `json:"collisions,omitempty"`
}

// MergeResult is the merged manifest set plus the per-document audit trail
// consumed by the merge preview (design §4/§7).
type MergeResult struct {
	Merged []unstructured.Unstructured `json:"merged"`
	Docs   []MergeDocReport            `json:"docs"`
}

// MergeManifests computes the three-way merge merge(base=LastApplied,
// ours=Manifests, theirs=Live) (design §4). Document sets align by (kind,
// namespace, name); leaves follow the adoption rules; lists are atomic —
// any change on either side replaces the whole list, both sides changing it
// is a collision — except containers, which merge by their name key.
// Collisions need a choice in resolutions and default to the DB side.
// Results never share structure with the inputs.
func MergeManifests(base, ours, theirs []unstructured.Unstructured, res MergeResolutions) (*MergeResult, error) {
	if res == nil {
		res = MergeResolutions{}
	}
	bIdx := indexDocs(base)
	oIdx := indexDocs(ours)
	tIdx := indexDocs(theirs)
	var order []string
	seen := map[string]bool{}
	addOrder := func(docs []unstructured.Unstructured) {
		for i := range docs {
			k := store.DocKeyOf(&docs[i])
			if !seen[k] {
				seen[k] = true
				order = append(order, k)
			}
		}
	}
	addOrder(ours)
	addOrder(theirs)
	addOrder(base)

	result := &MergeResult{Merged: []unstructured.Unstructured{}, Docs: []MergeDocReport{}}
	var firstErr error
	for _, key := range order {
		b, o, t := bIdx[key], oIdx[key], tIdx[key]
		ctx := newDocMergeCtx(res[key])
		rep := docReportHeader(b, o, t)
		switch {
		case o == nil && t == nil:
			// Deleted on both sides: the merge drops it.
			rep.Dropped = true
		case o == nil && b == nil:
			result.Merged = append(result.Merged, *t.DeepCopy())
			rep.OnlyIn = "theirs"
		case o == nil:
			// Deleted in the DB. Cluster unchanged → stays deleted;
			// cluster changed → collision between our deletion and their edit.
			if !deepEqualJSON(t.Object, b.Object) {
				appendMerged(result, ctx.collide(absent, absent, t.Object, ""))
			}
		case t == nil && b == nil:
			result.Merged = append(result.Merged, *o.DeepCopy())
			rep.OnlyIn = "ours"
		case t == nil:
			// Deleted in the cluster. DB unchanged → adopt the deletion as an
			// auto-adoption the caller may roll back; DB changed → collision.
			if deepEqualJSON(o.Object, b.Object) {
				if ctx.rollbacks[DocRootPath] {
					result.Merged = append(result.Merged, *b.DeepCopy())
					ctx.overridden = []string{DocRootPath}
				} else {
					ctx.adopted = []MergeAutoItem{{Path: DocRootPath, Base: presentable(b.Object)}}
				}
			} else {
				appendMerged(result, ctx.collide(absent, o.Object, absent, ""))
			}
		default:
			var baseV interface{} = absent
			if b != nil {
				baseV = b.Object
			}
			appendMerged(result, ctx.mergeValue(baseV, o.Object, t.Object, ""))
		}
		if ctx.err != nil && firstErr == nil {
			firstErr = ctx.err
		}
		rep.AutoAdopted = ctx.adopted
		rep.Overridden = ctx.overridden
		rep.Collisions = ctx.collisions
		result.Docs = append(result.Docs, rep)
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return result, nil
}

// appendMerged adds a merged document unless the merge yielded the absent
// marker (path deleted) or a non-object (cannot happen for object inputs).
func appendMerged(result *MergeResult, v interface{}) {
	if isAbsent(v) {
		return
	}
	m, ok := store.AsMap(v)
	if !ok {
		return
	}
	copied, _ := store.DeepCopyValue(m).(map[string]interface{})
	result.Merged = append(result.Merged, unstructured.Unstructured{Object: copied})
}

func indexDocs(docs []unstructured.Unstructured) map[string]*unstructured.Unstructured {
	out := make(map[string]*unstructured.Unstructured, len(docs))
	for i := range docs {
		out[store.DocKeyOf(&docs[i])] = &docs[i]
	}
	return out
}

func docReportHeader(b, o, t *unstructured.Unstructured) MergeDocReport {
	rep := MergeDocReport{}
	switch {
	case o != nil:
		rep.Kind, rep.Namespace, rep.Name = o.GetKind(), o.GetNamespace(), o.GetName()
	case t != nil:
		rep.Kind, rep.Namespace, rep.Name = t.GetKind(), t.GetNamespace(), t.GetName()
	case b != nil:
		rep.Kind, rep.Namespace, rep.Name = b.GetKind(), b.GetNamespace(), b.GetName()
	}
	return rep
}

// docMergeCtx carries one document's resolutions and accumulates its merge
// report. Paths reported externally are relative to the document, with
// DocRootPath standing for the document itself (the internal root path is
// the empty string).
type docMergeCtx struct {
	res        DocResolutions
	rollbacks  map[string]bool
	adopted    []MergeAutoItem
	overridden []string
	collisions []MergeCollision
	err        error
}

func newDocMergeCtx(res DocResolutions) *docMergeCtx {
	ctx := &docMergeCtx{res: res, rollbacks: map[string]bool{}}
	for _, p := range res.Rollbacks {
		ctx.rollbacks[p] = true
		if p == DocRootPath {
			ctx.rollbacks[""] = true
		}
	}
	return ctx
}

func reportPath(path string) string {
	if path == "" {
		return DocRootPath
	}
	return path
}

func joinKey(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func presentable(v interface{}) interface{} {
	if isAbsent(v) {
		return nil
	}
	return v
}

// mergeValue applies the leaf adoption rules at one path (design §4): the
// side that did not move away from the baseline yields; equal changes fold;
// both sides moving differently recurses into maps, merges containers by
// name, and collides on anything else. Recursion runs before the adoption
// shortcut even when only the cluster side moved, so adoptions are reported
// at the changed paths instead of one wholesale document item.
func (c *docMergeCtx) mergeValue(base, ours, theirs interface{}, path string) interface{} {
	if deepEqualJSON(theirs, base) {
		return ours
	}
	if deepEqualJSON(ours, theirs) {
		return ours
	}
	if om, oOK := store.AsMap(ours); oOK {
		if tm, tOK := store.AsMap(theirs); tOK {
			bm, bOK := store.AsMap(base)
			if bOK || isAbsent(base) || base == nil {
				if !bOK {
					bm = map[string]interface{}{}
				}
				out := make(map[string]interface{}, len(om)+len(tm))
				for _, k := range sortedUnionKeys(bm, om, tm) {
					mv := c.mergeValue(pick(bm, k), pick(om, k), pick(tm, k), joinKey(path, k))
					if !isAbsent(mv) {
						out[k] = mv
					}
				}
				return out
			}
		}
	} else if ol, oOK := store.AsList(ours); oOK {
		tl, tOK := store.AsList(theirs)
		bl, bOK := store.AsList(base)
		if tOK && (bOK || isAbsent(base) || base == nil) && isContainerPath(path) {
			return c.mergeContainers(bl, ol, tl, path)
		}
	}
	if deepEqualJSON(ours, base) {
		if c.rollbacks[path] {
			c.overridden = append(c.overridden, reportPath(path))
			return base
		}
		c.adopted = append(c.adopted, MergeAutoItem{
			Path:   reportPath(path),
			Base:   presentable(base),
			Theirs: presentable(theirs),
		})
		return theirs
	}
	return c.collide(base, ours, theirs, path)
}

// collide records a both-sides-changed leaf and applies the caller's choice,
// defaulting to the DB-side value when unresolved.
func (c *docMergeCtx) collide(base, ours, theirs interface{}, path string) interface{} {
	chosen, has := c.res.Choices[reportPath(path)]
	if has && chosen != ChooseOurs && chosen != ChooseTheirs {
		if c.err == nil {
			c.err = fmt.Errorf("merge: invalid choice %q for path %q", chosen, reportPath(path))
		}
		has = false
	}
	col := MergeCollision{
		Path:   reportPath(path),
		Base:   presentable(base),
		Ours:   presentable(ours),
		Theirs: presentable(theirs),
	}
	if has && chosen == ChooseTheirs {
		col.Chosen = ChooseTheirs
		c.collisions = append(c.collisions, col)
		return theirs
	}
	if has {
		col.Chosen = ChooseOurs
	}
	c.collisions = append(c.collisions, col)
	return ours
}

// mergeContainers merges a containers list by its name key (design §4):
// containers added on either side are adopted, deletions are adopted only
// when the other side did not modify the container, and shared containers
// merge field-wise. Lists without valid names fall back to a whole-list
// collision.
func (c *docMergeCtx) mergeContainers(base, ours, theirs []interface{}, listPath string) []interface{} {
	bIdx := elementsByName(base)
	oIdx := elementsByName(ours)
	tIdx := elementsByName(theirs)
	if bIdx == nil || oIdx == nil || tIdx == nil {
		mv := c.collide(base, ours, theirs, listPath)
		if l, ok := store.AsList(mv); ok {
			return l
		}
		return nil
	}
	var order []string
	seen := map[string]bool{}
	for _, e := range ours {
		if n := elemName(e); n != "" && !seen[n] {
			seen[n] = true
			order = append(order, n)
		}
	}
	for _, e := range theirs {
		if n := elemName(e); n != "" && !seen[n] {
			seen[n] = true
			order = append(order, n)
		}
	}
	out := make([]interface{}, 0, len(order))
	for _, n := range order {
		elemPath := fmt.Sprintf("%s[name=%s]", listPath, n)
		mv := c.mergeValue(elemOrAbsent(bIdx[n]), elemOrAbsent(oIdx[n]), elemOrAbsent(tIdx[n]), elemPath)
		if !isAbsent(mv) {
			out = append(out, mv)
		}
	}
	return out
}

func elementsByName(list []interface{}) map[string]map[string]interface{} {
	out := make(map[string]map[string]interface{}, len(list))
	for _, e := range list {
		m, ok := store.AsMap(e)
		if !ok {
			return nil
		}
		n, _ := m["name"].(string)
		if n == "" {
			return nil
		}
		out[n] = m
	}
	return out
}

func elemName(e interface{}) string {
	m, ok := store.AsMap(e)
	if !ok {
		return ""
	}
	n, _ := m["name"].(string)
	return n
}

func elemOrAbsent(m map[string]interface{}) interface{} {
	if m == nil {
		return absent
	}
	return m
}

// isContainerPath reports whether the path's last segment is a containers
// list, the one list the merge treats as name-keyed instead of atomic.
func isContainerPath(path string) bool {
	if i := strings.LastIndex(path, "."); i >= 0 {
		path = path[i+1:]
	}
	return path == "containers"
}
