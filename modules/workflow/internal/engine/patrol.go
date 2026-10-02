package engine

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/funcx27/kite/modules/workflow/internal/spec"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// sweepApprovalTimeouts settles approval nodes waiting past their configured
// timeout (design §5.3): reject (default) fails the node — the run driver
// then advances per the node's onFailure — approve completes it. Runs on the
// scheduler ticker; conditional updates settle races against a concurrent
// decide/cancel by affected-row count (design §6 幂等).
func (e *Engine) sweepApprovalTimeouts(nowT time.Time) {
	var runs []store.WorkflowRun
	if err := e.db.Select("id", "workflow_id", "run_number").
		Where("status = ?", store.RunRunning).Find(&runs).Error; err != nil {
		slog.Error("workflow: approval patrol load runs", "err", err)
		return
	}
	for _, run := range runs {
		e.sweepRunApprovalTimeouts(run, nowT)
	}
}

func (e *Engine) sweepRunApprovalTimeouts(run store.WorkflowRun, nowT time.Time) {
	dag, err := e.loadSpec(run.ID)
	if err != nil {
		slog.Error("workflow: approval patrol load spec", "run", run.ID, "err", err)
		return
	}
	var waiting []store.NodeRun
	if err := e.db.Where("run_id = ? AND status = ?", run.ID, store.NodeRunning).
		Find(&waiting).Error; err != nil {
		slog.Error("workflow: approval patrol load nodes", "run", run.ID, "err", err)
		return
	}
	byNode := make(map[string]store.NodeRun, len(waiting))
	for _, row := range waiting {
		byNode[row.NodeID] = row
	}
	for _, n := range dag.Nodes {
		if n.Type != spec.NodeApproval {
			continue
		}
		row, live := byNode[n.ID]
		if !live {
			continue
		}
		props, err := approvalProps(n)
		if err != nil {
			continue
		}
		d, err := time.ParseDuration(props.Timeout)
		if err != nil || d <= 0 || row.StartedAt == nil {
			continue
		}
		if nowT.Before(row.StartedAt.Add(d)) {
			continue
		}
		e.expireApproval(run, n.ID, row, props.TimeoutAction, d)
	}
}

// approvalProps decodes an approval node's raw props.
func approvalProps(n spec.Node) (spec.ApprovalProps, error) {
	var p spec.ApprovalProps
	b, err := json.Marshal(n.Props)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	return p, nil
}

// expireApproval settles one timed-out approval per timeoutAction and wakes
// the run driver; the audit entry keeps the trace (design §8).
func (e *Engine) expireApproval(run store.WorkflowRun, nodeID string, row store.NodeRun, action string, d time.Duration) {
	approve := strings.EqualFold(action, "approve")
	status := store.NodeFailed
	decision := "rejected"
	if approve {
		status = store.NodeSucceeded
		decision = "approved"
	}
	detail, err := json.Marshal(map[string]any{
		"decision": decision,
		"by":       0,
		"comment":  "approval timed out",
		"timedOut": true,
		"at":       time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	errMsg := ""
	if !approve {
		errMsg = store.Truncate(fmt.Sprintf("approval timed out after %s", d), store.ErrorMaxBytes)
	}
	hit, err := e.settleApproval(run.ID, row, status, detail, errMsg)
	if err != nil {
		slog.Error("workflow: approval timeout settle", "run", run.ID, "node", nodeID, "err", err)
		return
	}
	if !hit {
		return // decided or cancelled concurrently
	}
	e.auditApprovalTimeout(run, nodeID, decision)
}

// auditApprovalTimeout records one expiry in the host audit trail; the
// system (operator 0) is the acting party.
func (e *Engine) auditApprovalTimeout(run store.WorkflowRun, nodeID, decision string) {
	var wf store.Workflow
	if err := e.db.Select("name", "namespace").First(&wf, run.WorkflowID).Error; err != nil {
		slog.Error("workflow: approval timeout audit load workflow", "run", run.ID, "err", err)
		return
	}
	detail, err := json.Marshal(map[string]any{
		"run": run.ID, "node": nodeID, "decision": decision, "reason": "timeout",
	})
	if err != nil {
		return
	}
	e.deps.RecordHistory(store.HistoryEntry{
		ResourceType: authzResource,
		ResourceName: fmt.Sprintf("%s#%d", wf.Name, run.RunNumber),
		Namespace:    wf.Namespace,
		Operation:    "approval-timeout",
		YAML:         string(detail),
		Success:      true,
	})
}
