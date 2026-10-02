package application

import (
	"context"

	applicationapi "github.com/funcx27/kite/modules/application/internal/api"
	"github.com/funcx27/kite/modules/application/internal/store"
)

// Deploy facade (workflow design §5.2): the module's publish pipeline as one
// call for out-of-band callers. It reuses the exact HTTP deploy semantics —
// conflict gate, ensure-namespace, ownership-checked create, per-document
// RBAC under the operator's identity, anchor read-back — and never imports
// the workflow module or the host.

// ConflictDetail is one document's unresolved cluster-side drift paths
// reported by the publish conflict gate (design §6).
type ConflictDetail = applicationapi.ConflictDetail

// ConflictError reports the publish conflict gate verdict (design §6): merge
// the listed drift paths in the application module before retrying.
type ConflictError = applicationapi.ConflictError

// DocPreview is one changed document's redacted before/after view.
type DocPreview = applicationapi.DocPreview

// DeployOutcome is the deploy result: the revision the publish produced
// (0 = none persisted, i.e. dry-run or an imageless publish of the current
// config) and the change preview documents.
type DeployOutcome = applicationapi.DeployOutcome

// DeployWithImage publishes the application's config on the named cluster,
// persisting the image into the manifests first (design §6: 改镜像即发布);
// an empty image publishes the current config as-is. dryRun validates against
// the API server only. Permissions are checked with deps.AuthorizeUser under
// operatorID; conflicts come back as *ConflictError. onLog receives the
// publish header, one line per applied document and any conflict details;
// nil disables it.
func DeployWithImage(ctx context.Context, deps Deps, appName, cluster, image string, operatorID uint, dryRun bool, onLog func(string)) (*DeployOutcome, error) {
	return applicationapi.DeployWithImage(ctx, deps, store.New(deps.DB()), appName, cluster, image, operatorID, dryRun, onLog)
}
