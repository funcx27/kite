package engine

import (
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/funcx27/kite/modules/workflow/internal/spec"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// Cancel cancels a queued or running run (design §6 取消): the run row flips
// to cancelled, never-started nodes become skipped, waiting approvals are
// cancelled and in-flight task executions get their context cancelled — their
// goroutines settle their own rows.
func (e *Engine) Cancel(runID uint) error {
	var run store.WorkflowRun
	if err := e.db.Select("id", "workflow_id", "status").First(&run, runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	res := e.db.Model(&store.WorkflowRun{}).
		Where("id = ? AND status IN ?", runID, []string{store.RunQueued, store.RunRunning}).
		Updates(map[string]any{"status": store.RunCancelled, "finished_at": now()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrConflict
	}
	if dag, err := e.loadSpec(runID); err == nil {
		e.sweepNodes(runID, dag)
	}
	e.cancelRunContext(runID)
	e.signal(runID)
	return nil
}

// sweepNodes settles nodes that can never run: pending→skipped and waiting
// approvals→cancelled. Task rows in running are left to their goroutines.
func (e *Engine) sweepNodes(runID uint, dag spec.DAGSpec) {
	e.db.Model(&store.NodeRun{}).
		Where("run_id = ? AND status = ?", runID, store.NodePending).
		Updates(map[string]any{"status": store.NodeSkipped, "finished_at": now()})
	var approvalIDs []string
	for _, n := range dag.Nodes {
		if n.Type == spec.NodeApproval {
			approvalIDs = append(approvalIDs, n.ID)
		}
	}
	if len(approvalIDs) > 0 {
		e.db.Model(&store.NodeRun{}).
			Where("run_id = ? AND node_id IN ? AND status = ?", runID, approvalIDs, store.NodeRunning).
			Updates(map[string]any{"status": store.NodeCancelled, "finished_at": now()})
	}
}

// Retry resets a failed or cancelled run for re-execution (design §6 从失败
// 节点重试): failed/interrupted/cancelled nodes and their skipped downstream
// return to pending; succeeded nodes are never replayed. Attempt counters
// increment at dispatch, so a never-executed skipped node starts its first
// attempt at 1 like a fresh node. The run re-enters the queue and re-drives
// like a fresh trigger — after the previous driver has fully exited, so the
// two orchestrations never overlap.
func (e *Engine) Retry(runID uint) error {
	var run store.WorkflowRun
	if err := e.db.Select("id", "workflow_id", "revision_rev", "status").First(&run, runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	switch run.Status {
	case store.RunFailed, store.RunCancelled:
	default:
		return ErrConflict
	}
	rev, err := e.loadRevision(runID)
	if err != nil {
		return err
	}
	txErr := e.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&store.NodeRun{}).
			Where("run_id = ? AND status IN ?", runID, []string{
				store.NodeFailed, store.NodeInterrupted, store.NodeCancelled, store.NodeSkipped,
			}).
			Updates(map[string]any{
				"status":      store.NodePending,
				"error":       "",
				"log_tail":    "",
				"outputs":     nil,
				"started_at":  nil,
				"finished_at": nil,
			}).Error; err != nil {
			return err
		}
		res := tx.Model(&store.WorkflowRun{}).
			Where("id = ? AND status IN ?", runID, []string{store.RunFailed, store.RunCancelled}).
			Update("status", store.RunQueued)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrConflict
		}
		return nil
	})
	if txErr != nil {
		if errors.Is(txErr, ErrConflict) {
			return ErrConflict
		}
		return txErr
	}
	e.waitForDriver(runID)
	e.Enqueue(runID, run.WorkflowID, rev.Triggers)
	return nil
}

// Decide applies an approval decision (design §5.3): the waiting node flips
// to succeeded (approve) or failed with the comment as error (reject), the
// decision record lands in Outputs, and the run driver wakes. The caller
// (API) enforces the assignees-or-admin authorization; the conditional
// update settles races with a concurrent decision or a cancel.
func (e *Engine) Decide(runID uint, nodeID string, approve bool, comment string, operatorID uint) error {
	var row store.NodeRun
	if err := e.db.Where("run_id = ? AND node_id = ?", runID, nodeID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	dag, err := e.loadSpec(runID)
	if err != nil {
		return err
	}
	node, ok := dagNode(dag, nodeID)
	if !ok || node.Type != spec.NodeApproval {
		return ErrConflict
	}
	decision := "approved"
	status := store.NodeSucceeded
	if !approve {
		decision = "rejected"
		status = store.NodeFailed
	}
	outputs, err := json.Marshal(map[string]any{
		"decision": decision,
		"by":       operatorID,
		"comment":  comment,
		"at":       time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	updates := map[string]any{
		"status":      status,
		"outputs":     store.JSONField(outputs),
		"finished_at": now(),
	}
	if !approve {
		updates["error"] = store.Truncate(comment, store.ErrorMaxBytes)
	}
	res := e.db.Model(&store.NodeRun{}).
		Where("id = ? AND status = ?", row.ID, store.NodeRunning).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrConflict
	}
	e.signal(runID)
	return nil
}

func dagNode(dag spec.DAGSpec, id string) (spec.Node, bool) {
	for _, n := range dag.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return spec.Node{}, false
}
