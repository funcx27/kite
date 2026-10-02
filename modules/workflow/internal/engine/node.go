package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/funcx27/kite/modules/workflow/exec"
	"github.com/funcx27/kite/modules/workflow/internal/spec"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// logBuffer accumulates node output for LogTail, keeping only the tail under
// the store cap (design §3: 日志尾部截断存储).
type logBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (l *logBuffer) write(p string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b = append(l.b, p...)
	if len(l.b) > store.LogTailMaxBytes {
		l.b = append([]byte{}, l.b[len(l.b)-store.LogTailMaxBytes:]...)
	}
}

func (l *logBuffer) tail() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.b)
}

// execNode runs one task node to a terminal state: render the template
// inputs, execute via the injected executor, persist Outputs/Error/LogTail.
// Approval nodes never reach here — the driver parks them running and the
// approve/reject API decides them (design §5.3). Runs on its own goroutine
// holding one global dispatch slot.
func (e *Engine) execNode(parent context.Context, state *runState, node spec.Node, row store.NodeRun) {
	defer func() { <-e.slots; e.signal(state.run.ID) }()
	defer func() {
		if r := recover(); r != nil {
			// Per-node isolation: an executor panic fails the node instead
			// of leaking the dispatch slot and wedging the run.
			slog.Error("workflow: node executor panicked", "run", state.run.ID, "node", node.ID, "panic", r)
			e.completeNode(row.ID, row.Attempt, store.NodeFailed, nil, fmt.Errorf("node executor panicked: %v", r), "")
		}
	}()

	ctx := parent
	if d, err := time.ParseDuration(node.Timeout); err == nil && d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, d)
		defer cancel()
	}

	env := spec.ResolveEnv{
		Params: paramsMap(state.run.Params),
		Steps:  e.succeededOutputs(state.run.ID),
		Run: map[string]any{
			"number": state.run.RunNumber,
			"status": state.run.Status,
			"url":    fmt.Sprintf("/workflows/%s/runs/%d", state.wf.Name, state.run.ID),
		},
	}
	rendered, err := spec.Resolve(node.Props, env)
	if err != nil {
		// Template resolution failure = node failure with the reason
		// spelled out (design §4).
		e.completeNode(row.ID, row.Attempt, store.NodeFailed, nil, err, "")
		return
	}
	inputs, err := json.Marshal(rendered)
	if err == nil {
		e.db.Model(&store.NodeRun{}).
			Where("id = ? AND status = ?", row.ID, store.NodeRunning).
			Update("inputs", store.JSONField(inputs))
	}

	logs := &logBuffer{}
	var outputs map[string]any
	switch node.Type {
	case spec.NodeBuildImage:
		outputs, err = e.runBuild(ctx, rendered, state.run, logs)
	case spec.NodeDeployApp:
		outputs, err = e.runDeploy(ctx, rendered, state.run)
	case spec.NodeNotify:
		outputs, err = e.runNotify(ctx, rendered)
	case "":
		err = fmt.Errorf("node %q has no type", node.ID)
	default:
		err = fmt.Errorf("unknown node type %q", node.Type)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			// Run cancellation propagated into the executor: the node settles
			// as cancelled, not failed (design §6 state machine). A node-level
			// timeout surfaces as DeadlineExceeded and stays a failure.
			e.completeNode(row.ID, row.Attempt, store.NodeCancelled, outputs, nil, logs.tail())
			return
		}
		e.completeNode(row.ID, row.Attempt, store.NodeFailed, outputs, err, logs.tail())
		return
	}
	e.completeNode(row.ID, row.Attempt, store.NodeSucceeded, outputs, nil, logs.tail())
}

// runBuild drives a build-image node (design §5.1).
func (e *Engine) runBuild(ctx context.Context, rendered spec.Props, run store.WorkflowRun, logs *logBuffer) (map[string]any, error) {
	// Node-level authorization (design §8): the run executes with its
	// trigger user's RBAC identity; missing permission fails the node
	// loudly rather than skipping it. Buildfleet builds carry no cluster in
	// M1 props, so the check targets the host-wide builds capability — an
	// empty cluster only matches wildcard-cluster grants until M2 threads a
	// cluster through the build props.
	if !e.deps.AuthorizeUser(run.TriggeredBy, "builds", "create", "", "") {
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
	res, err := e.deps.BuildImage(ctx, exec.BuildRequest{
		Git:               source,
		HTTP:              httpSource,
		Dockerfile:        dockerfile,
		DockerfileContent: p.DockerfileContent,
		Args:              p.Args,
		Platforms:         p.Platforms,
		OutputRef:         p.OutputRef,
		CredentialRef:     p.CredentialRef,
		Provider:          p.Provider,
	}, logs.write)
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

// runDeploy drives a deploy-app node (design §5.2). The run executes with
// its trigger user's RBAC identity (design §7).
func (e *Engine) runDeploy(ctx context.Context, rendered spec.Props, run store.WorkflowRun) (map[string]any, error) {
	var p spec.DeployProps
	if err := decodeProps(rendered, &p); err != nil {
		return nil, fmt.Errorf("decode deploy props: %w", err)
	}
	if p.App == "" || p.Cluster == "" {
		return nil, fmt.Errorf("deploy node needs app and cluster")
	}
	res, err := e.deps.DeployApp(ctx, exec.DeployRequest{
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

// runNotify drives a notify node (design §5.4). The executor's failure fails
// the node but never blocks the pipeline — notify nodes carry forced continue
// semantics.
func (e *Engine) runNotify(ctx context.Context, rendered spec.Props) (map[string]any, error) {
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
	if err := e.deps.Notify(ctx, exec.NotifyRequest{URL: p.URL, Payload: payload}); err != nil {
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

// completeNode writes a node's terminal state via a conditional update,
// guarded by the dispatch attempt: a cancelled/retried racing actor wins by
// moving the row out of running first, and a stale executor goroutine from an
// earlier attempt cannot overwrite the new one's row (design §6 幂等).
func (e *Engine) completeNode(rowID uint, attempt int, status string, outputs map[string]any, err error, logTail string) {
	updates := map[string]any{
		"status":      status,
		"finished_at": now(),
		"log_tail":    logTail,
	}
	if outputs != nil {
		if b, merr := json.Marshal(outputs); merr == nil {
			updates["outputs"] = store.JSONField(b)
		}
	}
	if err != nil {
		updates["error"] = store.Truncate(err.Error(), store.ErrorMaxBytes)
	}
	if uerr := e.db.Model(&store.NodeRun{}).
		Where("id = ? AND status = ? AND attempt = ?", rowID, store.NodeRunning, attempt).
		Updates(updates).Error; uerr != nil {
		slog.Error("workflow: persist node completion", "node", rowID, "err", uerr)
	}
}

// decodeProps re-types rendered props (generic JSON) into a concrete props
// struct.
func decodeProps[T any](rendered spec.Props, out *T) error {
	b, err := json.Marshal(rendered)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}
