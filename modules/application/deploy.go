package application

import (
	"context"

	applicationapi "github.com/funcx27/kite/modules/application/internal/api"
	"github.com/funcx27/kite/modules/application/internal/store"
)

// Deploy facade (workflow design §5.2): the module's publish pipeline as one
// call for out-of-band callers. It reuses the exact HTTP deploy semantics —
// drift gate, ensure-namespace, ownership-checked create, per-document RBAC
// under the operator's identity, anchor read-back — and never imports the
// workflow module or the host.

// DocPreview is one changed document's redacted before/after view.
type DocPreview = applicationapi.DocPreview

// DriftError reports the workflow publish gate verdict: the live cluster
// state has unresolved drift against the anchor. Docs enumerates each drifted
// document with its changed leaf paths.
type DriftError = applicationapi.DriftError

// DeployOutcome is the deploy result: the revision the publish produced
// (0 = none persisted, i.e. dry-run or an imageless publish of the current
// config) and the change preview documents.
type DeployOutcome = applicationapi.DeployOutcome

// DeployWithImage publishes the application's config on the named cluster,
// persisting the image into the manifests first (design §6: 改镜像即发布);
// an empty image publishes the current config as-is. dryRun validates against
// the API server only. Permissions are checked with deps.AuthorizeUser under
// operatorID; unresolved cluster drift comes back as *DriftError. onLog
// receives the publish header and one line per applied document; nil
// disables it.
func DeployWithImage(ctx context.Context, deps Deps, appName, cluster, image string, operatorID uint, dryRun bool, onLog func(string)) (*DeployOutcome, error) {
	return applicationapi.DeployWithImage(ctx, deps, store.New(deps.DB()), appName, cluster, image, operatorID, dryRun, onLog)
}
