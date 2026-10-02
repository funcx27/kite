// Package engine implements the in-process DAG scheduler (design §6): one
// orchestration goroutine per run drives the ready set, dispatching node
// executions under a global concurrency cap. All state transitions go through
// conditional DB updates, so API calls racing the engine are settled by
// affected-row counts.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/funcx27/kite/modules/workflow/exec"
	"github.com/funcx27/kite/modules/workflow/internal/spec"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// MaxConcurrentNodes caps node executions in flight across all runs
// (design §6).
const MaxConcurrentNodes = 8

// Sentinel errors the API layer maps to HTTP statuses.
var (
	// ErrNotFound: the run or node does not exist.
	ErrNotFound = errors.New("workflow run not found")
	// ErrConflict: the target is not in a state allowing the transition.
	ErrConflict = errors.New("workflow run is not in a state allowing this action")
)

// Executors are the node execution capabilities injected from the module
// entry (design §5).
type Executors interface {
	BuildImage(ctx context.Context, req exec.BuildRequest, onLog func(string)) (exec.BuildResult, error)
	DeployApp(ctx context.Context, req exec.DeployRequest) (exec.DeployResult, error)
	Notify(ctx context.Context, req exec.NotifyRequest) error
}

// Deps is what the engine needs beyond the executors: identity-based
// authorization for node dispatch. Runs execute outside any request, so the
// check goes by user id, never a gin context (design §7/§8).
type Deps interface {
	Executors
	// AuthorizeUser reports whether the user may perform verb on resource
	// in cluster/namespace, judged against the host RBAC roles.
	AuthorizeUser(userID uint, resource, verb, cluster, namespace string) bool
}

// Engine schedules runs. One host process owns one Engine.
type Engine struct {
	deps   Deps
	db     *gorm.DB
	slots  chan struct{}
	drivers map[uint]*driverHandle

	mu sync.Mutex
	// active counts live orchestration goroutines per workflow (running and
	// queued-waiting), holding the workflow's concurrency budget.
	active map[uint]int
	// queue holds queued run ids per workflow, FIFO (design §6).
	queue map[uint][]uint
	// limits caches each workflow's per-run concurrency budget.
	limits map[uint]int
}

// driverHandle is one live orchestration of a run: its context, wake channel
// and completion signal. It is registered in Engine.drivers so API-driven
// actions can reach the live driver; the exiting driver deletes its
// registration only when it still owns it, so a superseding retry can never
// have its registration torn down by the predecessor it replaces.
type driverHandle struct {
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}
}

// New builds an Engine over the module tables and the injected capabilities.
func New(db *gorm.DB, deps Deps) *Engine {
	return &Engine{
		deps:    deps,
		db:      db,
		slots:   make(chan struct{}, MaxConcurrentNodes),
		drivers: map[uint]*driverHandle{},
		active:  map[uint]int{},
		queue:   map[uint][]uint{},
		limits:  map[uint]int{},
	}
}

// Start resumes non-terminal runs after a restart (design §6 重启恢复):
// running task nodes become interrupted, approval waits are preserved, then
// every recovered run is re-driven. Succeeded nodes are never replayed —
// state is the checkpoint.
func (e *Engine) Start() error {
	var runs []store.WorkflowRun
	if err := e.db.Where("status IN ?", []string{store.RunRunning, store.RunQueued}).Find(&runs).Error; err != nil {
		return err
	}
	// Group by workflow, keeping display order within each workflow.
	byWf := map[uint][]store.WorkflowRun{}
	for _, r := range runs {
		byWf[r.WorkflowID] = append(byWf[r.WorkflowID], r)
	}
	for wfID, wruns := range byWf {
		slices.SortFunc(wruns, func(a, b store.WorkflowRun) int { return a.RunNumber - b.RunNumber })
		var toDrive []uint
		e.mu.Lock()
		for _, r := range wruns {
			if r.Status == store.RunRunning {
				e.markInterrupted(r.ID)
				e.active[wfID]++
				toDrive = append(toDrive, r.ID)
			} else {
				e.queue[wfID] = append(e.queue[wfID], r.ID)
			}
		}
		// Recovered queued runs must drain too: without a live orchestration
		// there is no finishRun to pop them.
		toDrive = append(toDrive, e.drainLocked(wfID, e.limits[wfID])...)
		e.mu.Unlock()
		for _, id := range toDrive {
			go e.orchestrate(id, wfID)
		}
	}
	return nil
}

// markInterrupted flags task nodes left running by a process crash;
// approval nodes keep running — their wait is event-driven and survives
// restarts (design §3/§6).
func (e *Engine) markInterrupted(runID uint) {
	approvalIDs := e.approvalNodeIDs(runID)
	q := e.db.Model(&store.NodeRun{}).
		Where("run_id = ? AND status = ?", runID, store.NodeRunning)
	if len(approvalIDs) > 0 {
		q = q.Where("node_id NOT IN ?", approvalIDs)
	}
	q.Update("status", store.NodeInterrupted)
}

// approvalNodeIDs loads the approval-type node ids of a run's pinned spec.
func (e *Engine) approvalNodeIDs(runID uint) []string {
	dag, err := e.loadSpec(runID)
	if err != nil {
		return nil
	}
	var ids []string
	for _, n := range dag.Nodes {
		if n.Type == spec.NodeApproval {
			ids = append(ids, n.ID)
		}
	}
	return ids
}

// Enqueue admits a freshly created run: it starts immediately when the
// workflow's run budget allows, otherwise joins the FIFO queue (design §6).
func (e *Engine) Enqueue(runID, workflowID uint, triggersJSON []byte) {
	e.mu.Lock()
	e.limits[workflowID] = runLimit(triggersJSON)
	if e.active[workflowID] < e.limits[workflowID] {
		e.active[workflowID]++
		e.mu.Unlock()
		go e.orchestrate(runID, workflowID)
		return
	}
	e.queue[workflowID] = append(e.queue[workflowID], runID)
	e.mu.Unlock()
}

// runLimit reads the workflow's run budget from its Triggers JSON, clamped to
// the documented default when unset or invalid.
func runLimit(triggersJSON []byte) int {
	var t spec.TriggerConfig
	if err := json.Unmarshal(triggersJSON, &t); err != nil {
		return spec.DefaultMaxConcurrentRuns
	}
	if t.MaxConcurrent < 1 {
		return spec.DefaultMaxConcurrentRuns
	}
	return t.MaxConcurrent
}

// finishRun releases the workflow budget slot and drains the FIFO queue.
func (e *Engine) finishRun(workflowID uint) {
	e.mu.Lock()
	if e.active[workflowID] > 0 {
		e.active[workflowID]--
	}
	next := e.drainLocked(workflowID, e.limits[workflowID])
	e.mu.Unlock()
	for _, id := range next {
		go e.orchestrate(id, workflowID)
	}
}

// drainLocked pops queued runs into orchestration while the workflow budget
// allows; caller holds mu.
func (e *Engine) drainLocked(workflowID uint, limit int) []uint {
	if limit < 1 {
		limit = spec.DefaultMaxConcurrentRuns
	}
	var next []uint
	for e.active[workflowID] < limit && len(e.queue[workflowID]) > 0 {
		id := e.queue[workflowID][0]
		e.queue[workflowID] = e.queue[workflowID][1:]
		e.active[workflowID]++
		next = append(next, id)
	}
	return next
}

// orchestrate is the per-run driver loop: promote queued→running, then
// alternate dispatch passes with event waits until the run finalizes
// (design §6).
func (e *Engine) orchestrate(runID, workflowID uint) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &driverHandle{cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{})}
	e.mu.Lock()
	e.drivers[runID] = h
	e.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			// Per-run isolation: a driver bug must not wedge the run as
			// forever-running; fail it visibly instead.
			slog.Error("workflow: run driver panicked", "run", runID, "panic", r)
			e.finalizeRun(runID, store.RunFailed)
		}
		cancel()
		close(h.done)
		e.mu.Lock()
		if e.drivers[runID] == h {
			delete(e.drivers, runID)
		}
		e.mu.Unlock()
		e.finishRun(workflowID)
	}()

	if !e.promote(runID) {
		// Cancelled while queued, or a transient DB error left it queued for
		// the next recovery pass.
		return
	}
	errStreak := 0
	for {
		progressed, err := e.dispatchPass(ctx, runID)
		if err != nil {
			// Transient DB failures get a short bounded retry; past the
			// streak cap the run fails visibly — a bare return here would
			// leave it forever-running with no live wake channel, stranding
			// later API signals.
			errStreak++
			if errStreak >= 5 {
				slog.Error("workflow: run driver gave up after repeated dispatch errors", "run", runID, "err", err)
				e.finalizeRun(runID, store.RunFailed)
				return
			}
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
			}
			continue
		}
		errStreak = 0
		if e.checkTerminal(runID) {
			return
		}
		if progressed {
			continue
		}
		select {
		case <-h.wake:
		case <-ctx.Done():
		}
	}
}

// promote moves a queued run to running and stamps its start time via a
// conditional update. A run already running (recovered by Start) is driven
// as-is; false means the run is no longer runnable (cancelled while queued).
func (e *Engine) promote(runID uint) bool {
	res := e.db.Model(&store.WorkflowRun{}).
		Where("id = ? AND status = ?", runID, store.RunQueued).
		Updates(map[string]any{"status": store.RunRunning, "started_at": now()})
	if res.Error != nil {
		return false
	}
	if res.RowsAffected == 1 {
		return true
	}
	var status string
	if err := e.db.Model(&store.WorkflowRun{}).Where("id = ?", runID).
		Select("status").Scan(&status).Error; err != nil {
		return false
	}
	return status == store.RunRunning
}

// signal wakes a run's driver loop; coalesced via the buffered channel.
func (e *Engine) signal(runID uint) {
	e.mu.Lock()
	h := e.drivers[runID]
	e.mu.Unlock()
	if h != nil {
		select {
		case h.wake <- struct{}{}:
		default:
		}
	}
}

// waitForDriver blocks until the run's current orchestration exits (bounded
// wait), so a retry never overlaps the driver it supersedes — two live
// drivers on one run would race their registrations and double-dispatch.
func (e *Engine) waitForDriver(runID uint) {
	e.mu.Lock()
	h := e.drivers[runID]
	e.mu.Unlock()
	if h == nil {
		return
	}
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		slog.Warn("workflow: timed out waiting for the previous run driver to exit", "run", runID)
	}
}
