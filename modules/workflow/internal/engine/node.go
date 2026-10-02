package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

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
			"url":    fmt.Sprintf("/workflows/%s/runs/%d", state.wf.Name, state.run.ID),
		},
	}
	rendered, err := spec.Resolve(node.Props, env)
	if err != nil {
		// Template resolution failure = node failure with the reason
		// spelled out (design §4).
		e.completeNode(state.run.ID, node.ID, row.ID, row.Attempt, store.NodeFailed, nil, err, "")
		return
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
		outputs, err = runner.Run(ctx, rendered, state.run, onLog)
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
