package engine

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/funcx27/kite/modules/workflow/internal/spec"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// authzResource is the pseudo-resource engine-side audit entries attribute to
// (the api package checks request authorization against the same one).
const authzResource = "workflows"

// schedulerTick is the loop cadence: cron fires and approval timeouts are
// minute-granular (design §5.3/§7).
const schedulerTick = time.Minute

// startScheduler launches the one-minute loop driving cron triggers and the
// approval-timeout patrol; idempotent, and after a Stop it starts a fresh
// loop again. Deliberately no recover: like the process entry goroutines, a
// panic here fails fast into the host's restart-based self-healing rather
// than silently halting every workflow's triggers.
func (e *Engine) startScheduler() {
	e.schedMu.Lock()
	defer e.schedMu.Unlock()
	if e.schedStop != nil {
		return
	}
	e.schedStop = make(chan struct{})
	go e.scheduleLoop(e.schedStop)
}

// Stop halts the scheduler loop. Safe to call repeatedly; a later Start
// restarts the loop.
func (e *Engine) Stop() {
	e.schedMu.Lock()
	defer e.schedMu.Unlock()
	if e.schedStop != nil {
		close(e.schedStop)
		e.schedStop = nil
	}
}

func (e *Engine) scheduleLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(schedulerTick)
	defer ticker.Stop()
	// The window starts at loop launch: schedule points missed while the
	// process was down are skipped, not replayed (design §7 漏过即跳过).
	window := time.Now()
	for {
		select {
		case <-stop:
			return
		case t := <-ticker.C:
			e.fireCron(window, t)
			e.sweepApprovalTimeouts(t)
			window = t
		}
	}
}

// fireCron triggers every workflow whose cron schedule has a fire point
// inside (window, now]; multiple missed points within the window collapse
// into one run — the next window's points fire on their own tick.
func (e *Engine) fireCron(window, nowT time.Time) {
	var workflows []store.Workflow
	if err := e.db.Select("id", "name", "namespace", "current_rev").Find(&workflows).Error; err != nil {
		slog.Error("workflow: cron scheduler load workflows", "err", err)
		return
	}
	for i := range workflows {
		e.fireWorkflowCron(&workflows[i], window, nowT)
	}
}

func (e *Engine) fireWorkflowCron(wf *store.Workflow, window, nowT time.Time) {
	var rev store.WorkflowRevision
	if err := e.db.Select("triggers").
		Where("workflow_id = ? AND rev = ?", wf.ID, wf.CurrentRev).
		First(&rev).Error; err != nil {
		slog.Warn("workflow: cron scheduler load revision", "workflow", wf.Name, "err", err)
		return
	}
	var t spec.TriggerConfig
	if err := rev.Triggers.Unmarshal(&t); err != nil {
		// Triggers JSON was validated at save time; a corrupt row must be
		// visible instead of silently untriggered.
		slog.Warn("workflow: cron scheduler decode triggers", "workflow", wf.Name, "err", err)
		return
	}
	if t.Cron == "" {
		return
	}
	next, ok := spec.NextFire(t.Cron, window)
	if !ok {
		slog.Warn("workflow: cron schedule never fires, check the expression", "workflow", wf.Name, "cron", t.Cron)
		return
	}
	if next.After(nowT) {
		return
	}
	res, err := e.TriggerRun(wf, nil, store.TriggerCron, t.Cron, 0)
	if err != nil {
		// A due point that cannot trigger (missing defaults, empty DAG) is
		// dropped; the next scheduled point retries naturally.
		slog.Warn("workflow: cron trigger failed", "workflow", wf.Name, "err", err)
		return
	}
	e.auditTrigger(wf, res, "trigger-cron")
}

// auditTrigger records an engine-side run trigger in the host audit trail
// (design §8). Operation "trigger-cron"/"trigger-webhook" distinguishes
// non-interactive triggers; the run row itself carries the trigger type too.
func (e *Engine) auditTrigger(wf *store.Workflow, res TriggerResult, operation string) {
	detail, err := json.Marshal(map[string]any{"run": res.RunID})
	if err != nil {
		detail = nil
	}
	e.deps.RecordHistory(store.HistoryEntry{
		ResourceType: authzResource,
		ResourceName: fmt.Sprintf("%s#%d", wf.Name, res.RunNumber),
		Namespace:    wf.Namespace,
		Operation:    operation,
		YAML:         string(detail),
		Success:      true,
		OperatorID:   res.TriggeredBy,
	})
}
