package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// DeployOutcome is the deploy facade result: the revision the publish produced
// (0 — none persisted: dry-run or an imageless publish of the current config)
// and the redacted change preview documents.
type DeployOutcome struct {
	Revision int
	Preview  []DocPreview
}

// DriftError reports the workflow publish gate verdict: the live cluster
// state has drifted from the anchor on documents both sides still share, so
// the pending set may not apply until the drift is resolved (选边/同步/发布).
// Docs enumerates each drifted document with its changed leaf paths.
type DriftError struct {
	Docs []syncops.DocDiff
}

func (e *DriftError) Error() string {
	return "cluster has unresolved drift; resolve it in the application module (side-taking/sync/publish) and retry"
}

// publishGate is the workflow publish gate (workflow design §5.2): the diff
// between the anchor and the live cluster must be empty on every document
// both sides share. Never-deployed configs pass (empty anchor); documents
// missing on the cluster pass (the publish creates them); DB-side pending
// changes and the image bump do not participate — the gate compares the
// cluster only against the anchor.
func (h *Handler) publishGate(ctx context.Context, cli client.Client, cfg *store.AppConfig) error {
	anchor, err := store.DecodeManifests(cfg.LastApplied)
	if err != nil {
		return fmt.Errorf("decode anchor: %w", err)
	}
	if len(anchor) == 0 {
		return nil
	}
	live, err := syncops.FetchLiveDocs(ctx, cli, anchor)
	if err != nil {
		return fmt.Errorf("fetch live objects: %w", err)
	}
	var drift []syncops.DocDiff
	for _, d := range syncops.DiffManifests(anchor, syncops.LiveList(live, anchor)) {
		if len(d.Paths) > 0 {
			drift = append(drift, d)
		}
	}
	if len(drift) > 0 {
		return &DriftError{Docs: drift}
	}
	return nil
}

// DeployWithImage runs the application's 改镜像即发布 pipeline for an
// out-of-band identity (workflow design §5.2): resolve the cluster client,
// persist the image into the manifests as a Source=deploy-image revision,
// run the drift gate against the anchor, then the per-document-authorized
// apply with anchor read-back. A missing config is created from
// Render(App.Spec) first — row and rev1, gated on applications:create like
// the HTTP create funnels — and published like any other. dryRun validates
// against the API server only: the create-mode row is staged in memory,
// nothing is persisted (design §7). Permissions are checked with
// deps.AuthorizeUser under operatorID; unresolved cluster drift comes back
// as *DriftError; the caller's context cancellation aborts in-flight
// applies. onLog receives the publish header, one line per drifted document
// on a gated publish and one line per applied document; nil disables it.
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
	cli, err := deps.ResolveCluster(ctx, cluster)
	if err != nil {
		return nil, fmt.Errorf("cluster not found or offline: %s", cluster)
	}
	cfg, err := h.facadeTargetConfig(id, app, cluster, dryRun)
	if err != nil {
		return nil, err
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

	// The gate runs after the image persist but before any cluster write, so
	// a gated publish keeps its no-writes guarantee. The DriftError itself
	// enumerates the drifted documents; the adapter folds that list into the
	// node's error text, so it must not be logged here again.
	if err := h.publishGate(ctx, cli, cfg); err != nil {
		return nil, err
	}

	resp, err := h.applyPublish(ctx, id, app, cfg, cli, manifests, dryRun, "")
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
	return &DeployOutcome{Revision: latestRev, Preview: toDocPreviews(resp.Preview)}, nil
}

// facadeTargetConfig returns the config a facade publish applies to, creating
// it from the app's draft spec when the cluster has none yet (row + rev1).
// Landing a config row is a create whatever funnel does it, so the create
// path carries the same applications:create gate as the HTTP create funnels;
// everything else stays with applyPublish's per-document checks. With dryRun
// the row is only staged in memory — a dry-run never persists (design §7).
func (h *Handler) facadeTargetConfig(id identity, app *store.App, cluster string, dryRun bool) (*store.AppConfig, error) {
	cfg, err := h.findConfig(app.ID, cluster)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		if err != nil {
			return nil, fmt.Errorf("load config of application %s on cluster %s: %w", app.Name, cluster, err)
		}
		return cfg, nil
	}
	if !id.authorize(authzResource, "create", cluster, app.Namespace) {
		return nil, fmt.Errorf("user does not have permission to create %s in namespace %s on cluster %s",
			authzResource, app.Namespace, cluster)
	}
	docs, err := renderAppSpec(app)
	if err != nil {
		return nil, fmt.Errorf("render draft spec of application %s: %w", app.Name, err)
	}
	manifests, err := store.EncodeManifests(docs)
	if err != nil {
		return nil, fmt.Errorf("encode rendered manifests: %w", err)
	}
	if dryRun {
		return &store.AppConfig{
			AppID:     app.ID,
			Cluster:   cluster,
			Namespace: app.Namespace,
			Manifests: store.SecretJSON(manifests),
		}, nil
	}
	cfg, err = h.createConfigRow(id, app, cluster, manifests, store.RevSourceRender, "")
	if err != nil {
		return nil, fmt.Errorf("create config of application %s on cluster %s: %w", app.Name, cluster, err)
	}
	return cfg, nil
}

// toDocPreviews re-types the HTTP preview entries for the facade result.
func toDocPreviews(in []deployDocPreview) []DocPreview {
	out := make([]DocPreview, 0, len(in))
	for _, p := range in {
		out = append(out, DocPreview(p))
	}
	return out
}
