package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

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
// inputs, execute via the node type's registered runner, persist
// Outputs/Error/LogTail.
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
			e.completeNode(state.run.ID, node.ID, row.ID, row.Attempt, store.NodeFailed, nil, fmt.Errorf("node executor panicked: %v", r), "")
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
			"url":    fmt.Sprintf("/portal/workflows/%d/runs/%d", state.wf.ID, state.run.ID),
		},
		Ctx: e.templateCtx(state.wf),
	}
	// git.* variables resolve via ls-remote before the full render — the
	// commit must be known while the props still carry the {{git.*}} tokens.
	// Only build nodes referencing git.* pay the round trip.
	if node.Type == spec.NodeBuildImage && usesGitVars(node.Props) {
		if err := e.injectGitVars(ctx, state.run, node, &env); err != nil {
			e.completeNode(state.run.ID, node.ID, row.ID, row.Attempt, store.NodeFailed, nil, err, "")
			return
		}
	}
	// build.* variables need the same pre-render slot: the fingerprint covers
	// the dockerfile content and the rendered args, neither of which exists
	// before the full render.
	if node.Type == spec.NodeBuildImage && usesBuildVars(node.Props) {
		if err := e.injectBuildVars(ctx, state.run, node, &env); err != nil {
			e.completeNode(state.run.ID, node.ID, row.ID, row.Attempt, store.NodeFailed, nil, err, "")
			return
		}
	}
	rendered, err := spec.Resolve(node.Props, env)
	if err != nil {
		// Template resolution failure = node failure with the reason
		// spelled out (design §4).
		e.completeNode(state.run.ID, node.ID, row.ID, row.Attempt, store.NodeFailed, nil, err, "")
		return
	}
	if node.Type == spec.NodeBuildImage {
		// A git-var resolution pins the build to the commit the render used:
		// stamping it onto the git source lets the build runner verify (server
		// mode) or fetch exactly that SHA (runtime mode), closing the window
		// where a tag renders shortCommit abc123 but a moved branch tip builds
		// def456. Auto-named builds resolve their pin inside the runner.
		if env.Git != nil {
			if g, ok := rendered["git"].(map[string]any); ok {
				g["pinnedCommit"] = env.Git["commit"]
			}
		}
		// The build space follows the workflow's single space binding when the
		// props carry none (the form no longer edits it, and YAML saves never
		// injected it); an ambiguous or absent binding keeps the empty string.
		if s, _ := rendered["space"].(string); s == "" {
			rendered["space"] = env.Ctx["space"]
		}
		if env.Build != nil {
			rendered["resolvedFingerprint"] = env.Build["fingerprint"]
		}
	}
	inputs, err := json.Marshal(rendered)
	if err == nil {
		e.db.Model(&store.NodeRun{}).
			Where("id = ? AND status = ?", row.ID, store.NodeRunning).
			Update("inputs", store.JSONField(inputs))
	}

	// Log chunks land in the bounded tail buffer and on the run's SSE stream
	// at once (design §10); LogTail落库 keeps happening at completion only.
	logs := &logBuffer{}
	onLog := func(p string) {
		logs.write(p)
		e.publish(state.run.ID, RunEvent{Type: EventLog, NodeID: node.ID, Data: p})
	}

	var outputs map[string]any
	if node.Type == "" {
		err = fmt.Errorf("node %q has no type", node.ID)
	} else if runner, ok := e.runners[node.Type]; ok {
		outputs, err = runner.Run(ctx, rendered, state.run, state.wf, onLog)
	} else {
		err = fmt.Errorf("unknown node type %q", node.Type)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			// Run cancellation propagated into the executor: the node settles
			// as cancelled, not failed (design §6 state machine). A node-level
			// timeout surfaces as DeadlineExceeded and stays a failure.
			e.completeNode(state.run.ID, node.ID, row.ID, row.Attempt, store.NodeCancelled, outputs, nil, logs.tail())
			return
		}
		e.completeNode(state.run.ID, node.ID, row.ID, row.Attempt, store.NodeFailed, outputs, err, logs.tail())
		return
	}
	e.completeNode(state.run.ID, node.ID, row.ID, row.Attempt, store.NodeSucceeded, outputs, nil, logs.tail())
}

// usesGitVars reports whether the props reference any git.* template variable.
func usesGitVars(props spec.Props) bool {
	return slices.ContainsFunc(spec.TemplateRefs(props), func(ref string) bool {
		return strings.HasPrefix(ref, "git.")
	})
}

// injectGitVars resolves the node's git source to its commit SHA and populates
// env.Git for the subsequent full render. The source fields render first
// (they may carry {{params.*}}); the credential ref gets the same usage gate
// the build runner applies.
func (e *Engine) injectGitVars(ctx context.Context, run store.WorkflowRun, node spec.Node, env *spec.ResolveEnv) error {
	raw, ok := node.Props["git"].(map[string]any)
	if !ok {
		return fmt.Errorf("git template variables require a git source")
	}
	rendered, err := spec.Resolve(spec.Props{
		"url":              raw["url"],
		"ref":              raw["ref"],
		"gitCredentialRef": node.Props["gitCredentialRef"],
	}, *env)
	if err != nil {
		return fmt.Errorf("resolve git source: %w", err)
	}
	str := func(key string) string {
		s, _ := rendered[key].(string)
		return s
	}
	url, ref, credRef := str("url"), str("ref"), str("gitCredentialRef")
	if url == "" {
		return fmt.Errorf("git template variables require a git source url")
	}
	if credRef != "" && !e.deps.AuthorizeUser(run.TriggeredBy, credentialsResource, "get", "", "") {
		return fmt.Errorf("user %d does not have permission to use credential %q", run.TriggeredBy, credRef)
	}
	commit, err := e.deps.ResolveGitCommit(ctx, url, ref, credRef)
	if err != nil {
		return fmt.Errorf("resolve git commit: %w", err)
	}
	if commit == "" {
		shownRef := ref
		if shownRef == "" {
			shownRef = "HEAD"
		}
		return fmt.Errorf("could not resolve commit of %s@%s via ls-remote", url, shownRef)
	}
	env.Git = map[string]any{"commit": commit, "shortCommit": commit[:min(7, len(commit))]}
	return nil
}

// templateCtx assembles the workflow-scope ctx.* variables: the workflow
// name, the workflow's single space binding (empty with zero or several
// bindings — an ambiguous space must not silently pick one) and the default
// registry host (empty when unset or invalid).
func (e *Engine) templateCtx(wf store.Workflow) map[string]any {
	space := ""
	if bound, err := store.LoadBindings(e.db, []uint{wf.ID}, store.BindingKindSpace); err != nil {
		slog.Warn("workflow: load space bindings for template ctx", "workflow", wf.ID, "err", err)
	} else if len(bound[wf.ID]) == 1 {
		space = bound[wf.ID][0]
	}
	_, host, _ := e.deps.DefaultRegistry()
	return map[string]any{"workflow": wf.Name, "space": space, "registry": host}
}

// usesBuildVars reports whether the props reference any build.* template
// variable.
func usesBuildVars(props spec.Props) bool {
	return slices.ContainsFunc(spec.TemplateRefs(props), func(ref string) bool {
		return strings.HasPrefix(ref, "build.")
	})
}

// injectBuildVars resolves build.fingerprint into env.Build: the git source
// and the build args render first (they may carry {{params.*}}), then the
// dockerfile content — inline content directly, a path via the same probe the
// auto-naming runner uses — feeds the variant fingerprint. The probe reads at
// the ref's current tip; a ref that moves between this probe and the pinned
// build can shift the rendered fingerprint, a cosmetic mismatch the default
// (non-template) tag path rejects via its own moved-ref check.
func (e *Engine) injectBuildVars(ctx context.Context, run store.WorkflowRun, node spec.Node, env *spec.ResolveEnv) error {
	raw, ok := node.Props["git"].(map[string]any)
	if !ok {
		return fmt.Errorf("build template variables require a git source")
	}
	// build.* cannot appear inside these inputs themselves: env.Build is nil
	// during this partial render, so such a reference fails here.
	rendered, err := spec.Resolve(spec.Props{
		"url":              raw["url"],
		"ref":              raw["ref"],
		"subdir":           raw["subdir"],
		"mode":             raw["mode"],
		"gitCredentialRef": node.Props["gitCredentialRef"],
		"args":             node.Props["args"],
		"dockerfile":       node.Props["dockerfile"],
	}, *env)
	if err != nil {
		return fmt.Errorf("resolve build inputs: %w", err)
	}
	str := func(key string) string {
		s, _ := rendered[key].(string)
		return s
	}
	url, ref, credRef := str("url"), str("ref"), str("gitCredentialRef")
	if url == "" {
		return fmt.Errorf("build template variables require a git source url")
	}
	if credRef != "" && !e.deps.AuthorizeUser(run.TriggeredBy, credentialsResource, "get", "", "") {
		return fmt.Errorf("user %d does not have permission to use credential %q", run.TriggeredBy, credRef)
	}
	// One spec field, two wire shapes (the build runner's rule): whitespace
	// marks inline content, a single token a context path.
	v := strings.TrimSpace(str("dockerfile"))
	content := v
	if !strings.ContainsFunc(v, unicode.IsSpace) {
		path := v
		if path == "" {
			path = "Dockerfile"
		}
		probe, err := e.deps.ProbeDockerfile(ctx, url, ref, str("subdir"), path, str("mode"), credRef)
		if err != nil {
			return fmt.Errorf("fetch dockerfile for build.fingerprint: %w", err)
		}
		if !probe.Exists {
			return fmt.Errorf("build.fingerprint requires the dockerfile content, but %q was not found in the build context", path)
		}
		content = probe.Content
	}
	args := map[string]string{}
	if rawArgs, ok := rendered["args"].(map[string]any); ok {
		for k, val := range rawArgs {
			if s, ok := val.(string); ok {
				args[k] = s
			}
		}
	}
	env.Build = map[string]any{"fingerprint": variantFingerprint(content, args, str("subdir"))}
	return nil
}

// completeNode writes a node's terminal state via a conditional update,
// guarded by the dispatch attempt: a cancelled/retried racing actor wins by
// moving the row out of running first, and a stale executor goroutine from an
// earlier attempt cannot overwrite the new one's row (design §6 幂等).
func (e *Engine) completeNode(runID uint, nodeID string, rowID uint, attempt int, status string, outputs map[string]any, err error, logTail string) {
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
		return
	}
	e.publishNode(runID, nodeID, status)
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
