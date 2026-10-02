package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/funcx27/kite/modules/application/internal/store"
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

// DocPreview is one changed document's before/after view, Secret values
// redacted; the JSON shape matches the HTTP deploy response's preview entries.
type DocPreview struct {
	Kind   string          `json:"kind"`
	Name   string          `json:"name"`
	Action string          `json:"action"` // create | update
	Old    json.RawMessage `json:"old,omitempty"`
	New    json.RawMessage `json:"new,omitempty"`
}

// ConflictDetail is one document's unresolved cluster-side drift paths
// reported by the publish conflict gate (design §6).
type ConflictDetail = syncops.ConflictDetail

// ConflictError reports the publish conflict gate verdict (design §6): the
// pending set may not apply until every listed cluster-side drift path is
// merged in the application module. The message enumerates each conflict so
// out-of-band callers (workflow nodes) can surface the detail verbatim.
type ConflictError struct {
	Details []ConflictDetail
}

func (e *ConflictError) Error() string {
	var b strings.Builder
	b.WriteString("cluster has manual changes conflicting with the pending publish, merge first in the application module:")
	for _, d := range e.Details {
		fmt.Fprintf(&b, "\n  %s %s %s: %s", d.Kind, d.Namespace, d.Name, strings.Join(d.Paths, ", "))
	}
	return b.String()
}

// DeployOutcome is the deploy facade result: the revision the publish produced
// (0 — none persisted: dry-run or an imageless publish of the current config)
// and the redacted change preview documents.
type DeployOutcome struct {
	Revision int
	Preview  []DocPreview
}

// DeployWithImage runs the application's 改镜像即发布 pipeline for an
// out-of-band identity (workflow design §5.2): resolve the cluster client,
// persist the image into the manifests as a Source=deploy-image revision,
// then the conflict-gated, per-document-authorized apply with anchor
// read-back. dryRun validates against the API server only. Permissions are
// checked with deps.AuthorizeUser under operatorID; the caller's context
// cancellation aborts in-flight applies. onLog receives the publish header,
// one line per applied document and any conflict details; nil disables it.
func DeployWithImage(ctx context.Context, deps Deps, st *store.Store, appName, cluster, image string, operatorID uint, dryRun bool, onLog func(string)) (*DeployOutcome, error) {
	if onLog == nil {
		onLog = func(string) {}
	}
	logf := func(format string, args ...any) {
		onLog(fmt.Sprintf(format+"\n", args...))
	}
	h := &Handler{deps: deps, store: st}
	id := identity{
		operatorID: operatorID,
		authorize: func(resource, verb, c, ns string) bool {
			return deps.AuthorizeUser(operatorID, resource, verb, c, ns)
		},
	}

	app, err := h.findApp(appName)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("application not found: %s", appName)
		}
		return nil, fmt.Errorf("load application %s: %w", appName, err)
	}
	cfg, err := h.findConfig(app.ID, cluster)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("config not found for cluster %s", cluster)
		}
		return nil, fmt.Errorf("load config of application %s on cluster %s: %w", appName, cluster, err)
	}
	cli, err := deps.ResolveCluster(ctx, cluster)
	if err != nil {
		return nil, fmt.Errorf("cluster not found or offline: %s", cluster)
	}
	if image != "" {
		logf("publishing %s on %s (image %s)", appName, cluster, image)
	} else {
		logf("publishing %s on %s", appName, cluster)
	}
	stored, err := store.DecodeManifests(cfg.Manifests)
	if err != nil {
		return nil, fmt.Errorf("decode manifests: %w", err)
	}
	if len(stored) == 0 {
		return nil, errors.New("config has no manifests to deploy")
	}

	var (
		manifests []unstructured.Unstructured
		latestRev int
	)
	switch {
	case image != "" && dryRun:
		manifests, err = previewImage(stored, app.Name, image)
	case image != "":
		manifests, latestRev, err = h.persistImage(id, app, cfg, image)
	default:
		manifests = stored
	}
	if err != nil {
		return nil, err
	}

	resp, conflicts, err := h.runPublish(ctx, id, app, cfg, cli, manifests, dryRun)
	if err != nil {
		return nil, err
	}
	for _, r := range resp.Results {
		key := store.DocKey(r.Kind, r.Namespace, r.Name)
		if r.Success {
			logf("%s %s: %s", r.Kind, key, r.Action)
		} else {
			// applyDoc's error already carries the action and document key.
			logf("%s %s: %s", r.Kind, key, r.Error)
		}
	}
	if len(conflicts) > 0 {
		for _, d := range conflicts {
			logf("conflict %s %s: %s", store.DocKey(d.Kind, d.Namespace, d.Name), strings.Join(d.Paths, ", "))
		}
		return nil, &ConflictError{Details: conflicts}
	}
	return &DeployOutcome{Revision: latestRev, Preview: toDocPreviews(resp.Preview)}, nil
}

// toDocPreviews re-types the HTTP preview entries for the facade result.
func toDocPreviews(in []deployDocPreview) []DocPreview {
	out := make([]DocPreview, 0, len(in))
	for _, p := range in {
		out = append(out, DocPreview(p))
	}
	return out
}
