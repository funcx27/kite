package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/funcx27/kite/modules/workflow/internal/spec"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

func now() *time.Time { t := time.Now(); return &t }

// runState is one consistent view of a run: the run row, its pinned spec and
// the node rows the driver reads and mutates.
type runState struct {
	run   store.WorkflowRun
	wf    store.Workflow
	rev   store.WorkflowRevision
	dag   spec.DAGSpec
	nodes []store.NodeRun
}

func (s *runState) node(id string) (spec.Node, bool) {
	for _, n := range s.dag.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return spec.Node{}, false
}

// loadRevision fetches the pinned WorkflowRevision of a run.
func (e *Engine) loadRevision(runID uint) (*store.WorkflowRevision, error) {
	var run store.WorkflowRun
	if err := e.db.Select("id", "workflow_id", "revision_rev").First(&run, runID).Error; err != nil {
		return nil, err
	}
	var rev store.WorkflowRevision
	if err := e.db.Where("workflow_id = ? AND rev = ?", run.WorkflowID, run.RevisionRev).First(&rev).Error; err != nil {
		return nil, fmt.Errorf("load pinned revision %d: %w", run.RevisionRev, err)
	}
	return &rev, nil
}

// loadSpec fetches the DAGSpec a run pinned at trigger time.
func (e *Engine) loadSpec(runID uint) (spec.DAGSpec, error) {
	rev, err := e.loadRevision(runID)
	if err != nil {
		return spec.DAGSpec{}, err
	}
	var dag spec.DAGSpec
	if err := rev.Spec.Unmarshal(&dag); err != nil {
		return spec.DAGSpec{}, fmt.Errorf("decode spec of revision %d: %w", rev.Rev, err)
	}
	return dag, nil
}

// load assembles the full run state; gorm.ErrRecordNotFound maps to
// engine.ErrNotFound for API callers.
func (e *Engine) load(runID uint) (*runState, error) {
	var run store.WorkflowRun
	if err := e.db.First(&run, runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var wf store.Workflow
	if err := e.db.First(&wf, run.WorkflowID).Error; err != nil {
		return nil, err
	}
	var rev store.WorkflowRevision
	if err := e.db.Where("workflow_id = ? AND rev = ?", run.WorkflowID, run.RevisionRev).First(&rev).Error; err != nil {
		return nil, fmt.Errorf("load pinned revision %d: %w", run.RevisionRev, err)
	}
	var dag spec.DAGSpec
	if err := rev.Spec.Unmarshal(&dag); err != nil {
		return nil, fmt.Errorf("decode spec of revision %d: %w", rev.Rev, err)
	}
	var nodes []store.NodeRun
	if err := e.db.Where("run_id = ?", runID).Order("id").Find(&nodes).Error; err != nil {
		return nil, err
	}
	return &runState{run: run, wf: wf, rev: rev, dag: dag, nodes: nodes}, nil
}

// effectiveOnFailure: notify nodes are best-effort — they always run with
// continue semantics regardless of their configured policy (design §5.4).
// Deliberate driver-level policy, outside the runner registry: runners
// execute nodes but do not shape dispatch; approval is engine-native for the
// same reason (design §5).
func effectiveOnFailure(n spec.Node) string {
	if n.Type == spec.NodeNotify {
		return spec.OnFailureContinue
	}
	if n.OnFailure == "" {
		return spec.OnFailureStop
	}
	return n.OnFailure
}

// dispatchPass launches every ready node whose status allows a (re)start:
// pending or interrupted, with all predecessors succeeded (or failed under
// continue semantics). It reports whether it dispatched anything, so the
// driver loop re-polls immediately before waiting (design §6).
func (e *Engine) dispatchPass(ctx context.Context, runID uint) (bool, error) {
	state, err := e.load(runID)
	if err != nil {
		return false, err
	}
	if state.run.Status != store.RunRunning {
		return false, nil
	}
	byID := map[string]store.NodeRun{}
	for _, n := range state.nodes {
		byID[n.NodeID] = n
	}
	progressed := false
	for _, node := range state.dag.Nodes {
		row, ok := byID[node.ID]
		if !ok {
			continue
		}
		if row.Status != store.NodePending && row.Status != store.NodeInterrupted {
			continue
		}
		if !ready(state, byID, node.ID) {
			continue
		}
		switch node.Type {
		case spec.NodeApproval:
			// Approvals are event-driven: they occupy no goroutine and no
			// slot, they park in running until the approve/reject API call
			// (design §5.3).
			if e.markNodeRunning(&row) {
				progressed = true
			}
		default:
			// Global dispatch cap: when all slots are taken, stop the pass;
			// the completion signal of a running node re-drives the loop.
			select {
			case e.slots <- struct{}{}:
			default:
				return progressed, nil
			}
			if !e.markNodeRunning(&row) {
				<-e.slots
				continue
			}
			progressed = true
			go e.execNode(ctx, state, node, row)
		}
	}
	return progressed, nil
}

// ready reports whether every incoming edge of id is satisfied: the source
// succeeded, or failed under continue semantics (design §6 并行分支/汇合).
func ready(state *runState, byID map[string]store.NodeRun, id string) bool {
	for _, edge := range state.dag.Edges {
		if edge.To != id {
			continue
		}
		src, ok := byID[edge.From]
		if !ok {
			return false
		}
		switch src.Status {
		case store.NodeSucceeded:
		case store.NodeFailed:
			node, known := state.node(edge.From)
			if !known || effectiveOnFailure(node) != spec.OnFailureContinue {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// markNodeRunning transitions a dispatchable node into running via a
// conditional update, bumping its attempt counter; false means a concurrent
// actor (cancel sweep, retry) already moved it.
func (e *Engine) markNodeRunning(row *store.NodeRun) bool {
	res := e.db.Model(&store.NodeRun{}).
		Where("id = ? AND status IN ?", row.ID, []string{store.NodePending, store.NodeInterrupted}).
		Updates(map[string]any{"status": store.NodeRunning, "started_at": now(), "attempt": gorm.Expr("attempt + 1")})
	if res.Error == nil && res.RowsAffected == 1 {
		row.Attempt++
		return true
	}
	return false
}

// checkTerminal finalizes the run when no further progress is possible and
// reports whether it did (design §6 state machine):
//   - any failed node with stop semantics → run failed (pending→skipped,
//     in-flight work cancelled);
//   - a pending/interrupted/running node still exists → not terminal;
//   - otherwise every node reached succeeded/skipped or a continue-failure
//     → run succeeded.
func (e *Engine) checkTerminal(runID uint) bool {
	state, err := e.load(runID)
	if err != nil {
		return false
	}
	if state.run.Status != store.RunRunning {
		// Cancelled (or otherwise settled) by an API call; the sweep ran on
		// the cancel path.
		return true
	}
	byID := map[string]store.NodeRun{}
	for _, n := range state.nodes {
		byID[n.NodeID] = n
	}
	for _, node := range state.dag.Nodes {
		row, ok := byID[node.ID]
		if !ok {
			continue
		}
		if row.Status == store.NodeFailed && effectiveOnFailure(node) == spec.OnFailureStop {
			e.cancelRunContext(runID)
			e.sweepNodes(runID, state.dag)
			e.finalizeRun(runID, store.RunFailed)
			return true
		}
	}
	for _, row := range state.nodes {
		switch row.Status {
		case store.NodePending, store.NodeInterrupted, store.NodeRunning:
			return false
		}
	}
	e.finalizeRun(runID, store.RunSucceeded)
	return true
}

// cancelRunContext aborts in-flight node executions of a finalizing run.
func (e *Engine) cancelRunContext(runID uint) {
	e.mu.Lock()
	h := e.drivers[runID]
	e.mu.Unlock()
	if h != nil {
		h.cancel()
	}
}

// finalizeRun writes a terminal run status via a conditional update — the
// first writer wins (design §6 幂等).
func (e *Engine) finalizeRun(runID uint, status string) {
	res := e.db.Model(&store.WorkflowRun{}).
		Where("id = ? AND status IN ?", runID, []string{store.RunRunning, store.RunQueued}).
		Updates(map[string]any{"status": status, "finished_at": now()})
	if res.Error != nil {
		slog.Error("workflow: finalize run", "run", runID, "status", status, "err", res.Error)
	}
}

// succeededOutputs collects the Outputs of a run's succeeded nodes — the
// steps.* side of the template environment (design §4).
func (e *Engine) succeededOutputs(runID uint) map[string]map[string]any {
	var rows []store.NodeRun
	if err := e.db.Select("node_id", "outputs").
		Where("run_id = ? AND status = ?", runID, store.NodeSucceeded).
		Find(&rows).Error; err != nil {
		return map[string]map[string]any{}
	}
	out := map[string]map[string]any{}
	for _, r := range rows {
		outputs := map[string]any{}
		_ = r.Outputs.Unmarshal(&outputs)
		out[r.NodeID] = outputs
	}
	return out
}

// paramsMap decodes the run's parameter snapshot.
func paramsMap(raw []byte) map[string]any {
	params := map[string]any{}
	_ = store.JSONField(raw).Unmarshal(&params)
	return params
}
