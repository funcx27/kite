// Package drift computes the on-demand drift report of a cluster config
// (design §4/§7): the two diff groups against the LastApplied anchor, the
// suggested action and the StatusSummary value the API writes back.
package drift

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/funcx27/kite/modules/application/internal/store"
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

// Suggested actions of a drift report (design §4.4). merge covers both the
// cluster-side-only adoption (the merge degenerates into a whole adoption
// when the DB has no pending changes) and the conflict resolution through
// the merge preview — the design keeps one verb for both scenarios.
const (
	ActionNone    = "none"
	ActionPublish = "publish"
	ActionMerge   = "merge"
)

// Result is the computed drift report. Drift is diff(live, lastApplied) —
// cluster-side drift; Pending is diff(manifests, lastApplied) — DB-side
// changes awaiting publish (design §4.4).
type Result struct {
	Status  string            `json:"status"`
	Action  string            `json:"action,omitempty"`
	Drift   []syncops.DocDiff `json:"drift"`
	Pending []syncops.DocDiff `json:"pending"`
}

// Check computes the drift report from the stored manifests and anchor plus
// the live objects fetched by the caller; a nil live map yields no
// cluster-side diff (undeployed configs have nothing to compare against).
// overridden carries the scoped knowing-override paths (design §4). The
// StatusSummary value derives from which sides differ: with cluster-side
// drift present but every drift path adjudicated — adopted into the merged
// manifests or knowingly overridden, i.e. the publish gate would pass — the
// config is in the merged state a confirmed merge leaves behind (design
// §3/§4 合并→发布); unresolved drift on both sides is conflict, one side is
// drift, none is synced; an empty anchor means undeployed.
func Check(manifests, anchor []unstructured.Unstructured, live map[string]*unstructured.Unstructured, overridden map[string]bool) Result {
	clusterSide := clusterSideDiff(anchor, live)
	// Re-normalize both sides here, not only at anchor write time: anchors
	// stored before a Normalize strip-list extension keep their old server
	// defaults, and normalizing on read keeps the report clean without a
	// republish (design §4.3).
	pending := syncops.DiffManifests(syncops.NormalizeManifests(manifests), syncops.NormalizeManifests(anchor))
	res := Result{Drift: clusterSide, Pending: pending}
	switch {
	case len(anchor) == 0:
		res.Status, res.Action = store.StatusUndeployed, ActionPublish
	case len(clusterSide) > 0 && len(syncops.ConflictGate(manifests, anchor, live, overridden)) == 0:
		res.Status, res.Action = store.StatusMerged, ActionPublish
	case len(clusterSide) > 0 && len(pending) > 0:
		res.Status, res.Action = store.StatusConflict, ActionMerge
	case len(clusterSide) > 0:
		res.Status, res.Action = store.StatusDrift, ActionMerge
	case len(pending) > 0:
		res.Status, res.Action = store.StatusDrift, ActionPublish
	default:
		res.Status, res.Action = store.StatusSynced, ActionNone
	}
	return res
}

// clusterSideDiff diffs the live objects against the anchor (design §4.4).
// Anchor documents missing on the cluster surface as MissingIn "a", the same
// convention the deploy response uses for its drift group.
func clusterSideDiff(anchor []unstructured.Unstructured, live map[string]*unstructured.Unstructured) []syncops.DocDiff {
	liveDocs := make([]unstructured.Unstructured, 0, len(anchor))
	anchorDocs := make([]unstructured.Unstructured, 0, len(anchor))
	for i := range anchor {
		ad := &anchor[i]
		ld := live[store.DocKeyOf(ad)]
		if ld != nil {
			liveDocs = append(liveDocs, *ld)
		}
		anchorDocs = append(anchorDocs, *ad)
	}
	return syncops.DiffManifests(syncops.NormalizeManifests(liveDocs), syncops.NormalizeManifests(anchorDocs))
}
