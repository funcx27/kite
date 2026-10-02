package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/funcx27/kite/modules/workflow/exec"
	"github.com/funcx27/kite/modules/workflow/internal/spec"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// NodeRunner executes one task-node type. Decoding and checking the rendered
// props is Run's first step — save-time validation lives in spec.Validate, so
// the interface carries no ValidateProps. Approval is engine-native
// (event-driven, design §5.3) and never reaches a runner.
type NodeRunner interface {
	Run(ctx context.Context, rendered spec.Props, run store.WorkflowRun, onLog func(string)) (map[string]any, error)
}

// buildDeps is the capability slice buildRunner consumes.
type buildDeps interface {
	AuthorizeUser(userID uint, resource, verb, cluster, namespace string) bool
	BuildImage(ctx context.Context, req exec.BuildRequest, onLog func(string)) (exec.BuildResult, error)
}

// buildRunner drives a build-image node (design §5.1).
type buildRunner struct {
	deps buildDeps
}

// Run implements NodeRunner for spec.NodeBuildImage.
func (r buildRunner) Run(ctx context.Context, rendered spec.Props, run store.WorkflowRun, onLog func(string)) (map[string]any, error) {
	// Node-level authorization (design §8): the run executes with its
	// trigger user's RBAC identity; missing permission fails the node
	// loudly rather than skipping it. Buildfleet builds carry no cluster in
	// M1 props, so the check targets the host-wide builds capability — an
	// empty cluster only matches wildcard-cluster grants until M2 threads a
	// cluster through the build props.
	if !r.deps.AuthorizeUser(run.TriggeredBy, "builds", "create", "", "") {
		return nil, fmt.Errorf("user %d does not have permission to create builds", run.TriggeredBy)
	}
	var p spec.BuildProps
	if err := decodeProps(rendered, &p); err != nil {
		return nil, fmt.Errorf("decode build props: %w", err)
	}
	var source *exec.GitSource
	var httpSource *exec.HTTPSource
	switch {
	case p.Git != nil && p.Git.URL != "":
		source = &exec.GitSource{URL: p.Git.URL, Ref: p.Git.Ref, Subdir: p.Git.Subdir}
	case p.HTTP != nil && p.HTTP.URL != "":
		httpSource = &exec.HTTPSource{URL: p.HTTP.URL}
	default:
		return nil, fmt.Errorf("build node needs a git or http source")
	}
	if p.OutputRef == "" {
		return nil, fmt.Errorf("build node needs outputRef")
	}
	dockerfile := p.Dockerfile
	if dockerfile == "" && p.DockerfileContent == "" {
		dockerfile = "Dockerfile"
	}
	res, err := r.deps.BuildImage(ctx, exec.BuildRequest{
		Git:               source,
		HTTP:              httpSource,
		Dockerfile:        dockerfile,
		DockerfileContent: p.DockerfileContent,
		Args:              p.Args,
		Platforms:         p.Platforms,
		OutputRef:         p.OutputRef,
		CredentialRef:     p.CredentialRef,
		Provider:          p.Provider,
	}, onLog)
	if err != nil {
		return nil, err
	}
	return buildOutputs(res), nil
}

// buildOutputs shapes the build executor result into the node Outputs; the
// digest-carrying reference is the one downstream deploy nodes should
// consume (design §5.1).
func buildOutputs(res exec.BuildResult) map[string]any {
	out := map[string]any{
		"imageRef": res.ImageRef,
		"digest":   res.Digest,
	}
	if res.ImageRef != "" && res.Digest != "" {
		base := res.ImageRef
		if i := strings.LastIndex(base, ":"); i > strings.LastIndex(base, "/") {
			base = base[:i]
		}
		out["imageRefWithDigest"] = base + "@" + res.Digest
	}
	return out
}

// deployDeps is the capability slice deployRunner consumes.
type deployDeps interface {
	DeployApp(ctx context.Context, req exec.DeployRequest) (exec.DeployResult, error)
}

// deployRunner drives a deploy-app node (design §5.2). The run executes with
// its trigger user's RBAC identity (design §7).
type deployRunner struct {
	deps deployDeps
}

// Run implements NodeRunner for spec.NodeDeployApp.
func (r deployRunner) Run(ctx context.Context, rendered spec.Props, run store.WorkflowRun, onLog func(string)) (map[string]any, error) {
	var p spec.DeployProps
	if err := decodeProps(rendered, &p); err != nil {
		return nil, fmt.Errorf("decode deploy props: %w", err)
	}
	if p.App == "" || p.Cluster == "" {
		return nil, fmt.Errorf("deploy node needs app and cluster")
	}
	res, err := r.deps.DeployApp(ctx, exec.DeployRequest{
		App:        p.App,
		Cluster:    p.Cluster,
		Image:      p.Image,
		DryRun:     p.DryRun,
		OperatorID: run.TriggeredBy,
	})
	if err != nil {
		return nil, err
	}
	var preview any = res.Preview
	if len(res.Preview) > 0 {
		var docs any
		if json.Unmarshal(res.Preview, &docs) == nil {
			preview = docs
		}
	}
	return map[string]any{"revision": res.Revision, "preview": preview}, nil
}

// notifyDeps is the capability slice notifyRunner consumes.
type notifyDeps interface {
	Notify(ctx context.Context, req exec.NotifyRequest) error
}

// notifyRunner drives a notify node (design §5.4). The executor's failure
// fails the node but never blocks the pipeline — notify nodes carry forced
// continue semantics.
type notifyRunner struct {
	deps notifyDeps
}

// Run implements NodeRunner for spec.NodeNotify.
func (r notifyRunner) Run(ctx context.Context, rendered spec.Props, run store.WorkflowRun, onLog func(string)) (map[string]any, error) {
	var p spec.NotifyProps
	if err := decodeProps(rendered, &p); err != nil {
		return nil, fmt.Errorf("decode notify props: %w", err)
	}
	if p.URL == "" {
		return nil, fmt.Errorf("notify node needs url")
	}
	payload, err := notifyPayload(p.PayloadTemplate)
	if err != nil {
		return nil, err
	}
	if err := r.deps.Notify(ctx, exec.NotifyRequest{URL: p.URL, Payload: payload}); err != nil {
		return nil, fmt.Errorf("notify: %w", err)
	}
	return map[string]any{"delivered": true}, nil
}

// notifyPayload wraps the rendered template into the webhook body: a valid
// JSON template is sent as-is, plain text becomes {"text": ...}.
func notifyPayload(template string) ([]byte, error) {
	if template == "" {
		return []byte(`{}`), nil
	}
	if json.Valid([]byte(template)) {
		return []byte(template), nil
	}
	b, err := json.Marshal(map[string]string{"text": template})
	if err != nil {
		return nil, err
	}
	return b, nil
}
