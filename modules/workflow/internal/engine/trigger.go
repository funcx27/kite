package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/funcx27/kite/modules/workflow/internal/spec"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// Sentinel errors the API layer maps to HTTP statuses.
var (
	// ErrNoNodes: the pinned revision has an empty DAG.
	ErrNoNodes = errors.New("workflow has no nodes to run")
	// ErrTriggerDisabled: the requested trigger surface is turned off in the
	// revision's TriggerConfig.
	ErrTriggerDisabled = errors.New("trigger surface is disabled for this workflow")
	// ErrInvalidTrigger: the raw parameters fail the revision's schema;
	// wrapped details name the problem.
	ErrInvalidTrigger = errors.New("trigger parameters are invalid")
)

// TriggerResult reports a successful trigger: the created run and the
// resolved parameter snapshot the audit trail records.
type TriggerResult struct {
	RunID       uint
	RunNumber   int
	TriggeredBy uint
	Params      []byte
}

// TriggerRun creates and admits a run of wf's current revision (design §7):
// the run pins the revision, the raw parameters resolve against the schema
// (raw=nil fills schema defaults), and admission goes through the workflow's
// concurrency budget like any other trigger. triggeredBy is the run's RBAC
// identity; zero selects the revision operator — the last editor of the
// trigger config, the documented identity for webhook/cron triggers.
func (e *Engine) TriggerRun(wf *store.Workflow, raw map[string]any, triggerType, triggerRef string, triggeredBy uint) (TriggerResult, error) {
	plan, err := e.planTrigger(wf, raw, triggerType, triggeredBy)
	if err != nil {
		return TriggerResult{}, err
	}
	var runID uint
	var runNumber int
	err = e.db.Transaction(func(tx *gorm.DB) error {
		var txErr error
		runID, runNumber, txErr = store.CreateRunTx(tx, wf.ID, wf.CurrentRev,
			plan.paramsJSON, triggerType, triggerRef, plan.triggeredBy, plan.nodeIDs)
		return txErr
	})
	if err != nil {
		return TriggerResult{}, err
	}
	if err := store.PruneRuns(e.db, wf.ID, runNumber); err != nil {
		// The run exists; retention trimming is best-effort.
		slog.Error("workflow: prune runs", "workflow", wf.ID, "err", err)
	}
	e.Enqueue(runID, wf.ID, []byte(plan.rev.Triggers))
	return TriggerResult{
		RunID: runID, RunNumber: runNumber, TriggeredBy: plan.triggeredBy, Params: plan.paramsJSON,
	}, nil
}

// triggerPlan is the resolved shape one run creation needs from the pinned
// revision.
type triggerPlan struct {
	rev         store.WorkflowRevision
	paramsJSON  []byte
	triggeredBy uint
	nodeIDs     []string
}

// planTrigger loads wf's current revision and validates the trigger against
// it: surface enabled, DAG runnable, parameters resolvable.
func (e *Engine) planTrigger(wf *store.Workflow, raw map[string]any, triggerType string, triggeredBy uint) (triggerPlan, error) {
	var rev store.WorkflowRevision
	if err := e.db.Where("workflow_id = ? AND rev = ?", wf.ID, wf.CurrentRev).First(&rev).Error; err != nil {
		return triggerPlan{}, err
	}
	var dag spec.DAGSpec
	if err := rev.Spec.Unmarshal(&dag); err != nil {
		return triggerPlan{}, fmt.Errorf("decode spec of revision %d: %w", rev.Rev, err)
	}
	if len(dag.Nodes) == 0 {
		return triggerPlan{}, ErrNoNodes
	}
	var triggers spec.TriggerConfig
	if err := rev.Triggers.Unmarshal(&triggers); err != nil {
		return triggerPlan{}, fmt.Errorf("decode triggers of revision %d: %w", rev.Rev, err)
	}
	if !triggerEnabled(triggers, triggerType) {
		return triggerPlan{}, ErrTriggerDisabled
	}
	var schema []spec.ParamSpec
	if err := rev.Params.Unmarshal(&schema); err != nil {
		return triggerPlan{}, fmt.Errorf("decode params of revision %d: %w", rev.Rev, err)
	}
	resolved, err := spec.ResolveParams(schema, raw)
	if err != nil {
		return triggerPlan{}, fmt.Errorf("%w: %w", ErrInvalidTrigger, err)
	}
	paramsJSON, err := json.Marshal(resolved)
	if err != nil {
		return triggerPlan{}, err
	}
	if triggeredBy == 0 {
		triggeredBy = rev.OperatorID
	}
	nodeIDs := make([]string, 0, len(dag.Nodes))
	for _, n := range dag.Nodes {
		nodeIDs = append(nodeIDs, n.ID)
	}
	return triggerPlan{rev: rev, paramsJSON: paramsJSON, triggeredBy: triggeredBy, nodeIDs: nodeIDs}, nil
}

// triggerEnabled reports whether triggerType is switched on in the config.
func triggerEnabled(t spec.TriggerConfig, triggerType string) bool {
	switch triggerType {
	case store.TriggerManual:
		return t.ManualEnabled()
	case store.TriggerWebhook:
		return t.Webhook
	case store.TriggerCron:
		return t.Cron != ""
	default:
		return false
	}
}
