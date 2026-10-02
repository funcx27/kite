package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/funcx27/kite/modules/workflow/internal/engine"
	"github.com/funcx27/kite/modules/workflow/internal/spec"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// runSummary is the list view of a run.
type runSummary struct {
	ID              uint            `json:"id"`
	RunNumber       int             `json:"runNumber"`
	RevisionRev     int             `json:"revisionRev"`
	Status          string          `json:"status"`
	TriggerType     string          `json:"triggerType"`
	TriggeredBy     uint            `json:"triggeredBy"`
	TriggeredByName string          `json:"triggeredByName,omitempty"`
	TriggerRef      string          `json:"triggerRef,omitempty"`
	Params          json.RawMessage `json:"params,omitempty"`
	StartedAt       *time.Time      `json:"startedAt"`
	FinishedAt      *time.Time      `json:"finishedAt"`
}

// TriggerRun admits a manual run (design §7): parameters are validated
// against the current revision's schema with defaults applied, the run pins
// the current revision and expands all NodeRun rows pending, and the engine
// schedules it. The run executes with the trigger user's RBAC identity
// (design §7).
func (h *Handler) TriggerRun(c *gin.Context) {
	wf, ok := h.findWorkflow(c)
	if !ok {
		return
	}
	if !h.authorize(c, "exec", c.GetString(clusterNameContextKey), wf.Namespace) {
		return
	}
	var req struct {
		Params map[string]any `json:"params"`
		Ref    string         `json:"ref"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			h.fail(c, http.StatusBadRequest, err, "")
			return
		}
	}
	paramsJSON, triggersJSON, nodeIDs, ok := h.prepareTrigger(c, wf, req.Params)
	if !ok {
		return
	}
	var runID uint
	var runNumber int
	err := h.store.DB().Transaction(func(tx *gorm.DB) error {
		var txErr error
		runID, runNumber, txErr = store.CreateRunTx(tx, wf.ID, wf.CurrentRev, paramsJSON,
			store.TriggerManual, req.Ref, h.deps.OperatorID(c), nodeIDs)
		return txErr
	})
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if err := store.PruneRuns(h.store.DB(), wf.ID, runNumber); err != nil {
		// The run exists; retention trimming is best-effort.
		slog.Error("workflow: prune runs", "workflow", wf.ID, "err", err)
	}
	h.engine.Enqueue(runID, wf.ID, triggersJSON)
	h.recordAudit(c, store.HistoryEntry{
		Cluster: c.GetString(clusterNameContextKey), ResourceType: authzResource,
		ResourceName: fmt.Sprintf("%s#%d", wf.Name, runNumber),
		Namespace:    wf.Namespace, Operation: "trigger",
		YAML: string(paramsJSON),
	})
	c.JSON(http.StatusCreated, gin.H{
		"id": runID, "runNumber": runNumber, "status": store.RunQueued, "revisionRev": wf.CurrentRev,
	})
}

// prepareTrigger validates a manual trigger against the current revision:
// the DAG must be runnable, manual must be enabled and the raw parameters
// resolve against the schema. Returns the encoded values the run creation
// transaction needs; ok=false means the response is already written.
func (h *Handler) prepareTrigger(c *gin.Context, wf *store.Workflow, raw map[string]any) (paramsJSON, triggersJSON []byte, nodeIDs []string, ok bool) {
	var rev store.WorkflowRevision
	if err := h.store.DB().Where("workflow_id = ? AND rev = ?", wf.ID, wf.CurrentRev).First(&rev).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return nil, nil, nil, false
	}
	var dag spec.DAGSpec
	if err := rev.Spec.Unmarshal(&dag); err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return nil, nil, nil, false
	}
	if len(dag.Nodes) == 0 {
		h.fail(c, http.StatusBadRequest, nil, "workflow has no nodes to run")
		return nil, nil, nil, false
	}
	var params []spec.ParamSpec
	if err := rev.Params.Unmarshal(&params); err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return nil, nil, nil, false
	}
	var triggers spec.TriggerConfig
	_ = rev.Triggers.Unmarshal(&triggers)
	if !triggers.ManualEnabled() {
		h.fail(c, http.StatusConflict, nil, "manual triggers are disabled for this workflow")
		return nil, nil, nil, false
	}
	resolved, err := spec.ResolveParams(params, raw)
	if err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return nil, nil, nil, false
	}
	paramsJSON, err = json.Marshal(resolved)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return nil, nil, nil, false
	}
	nodeIDs = make([]string, 0, len(dag.Nodes))
	for _, n := range dag.Nodes {
		nodeIDs = append(nodeIDs, n.ID)
	}
	return paramsJSON, []byte(rev.Triggers), nodeIDs, true
}

// ListRuns returns a workflow's runs, newest first (design §10: 倒序分页).
func (h *Handler) ListRuns(c *gin.Context) {
	wf, ok := h.findWorkflow(c)
	if !ok {
		return
	}
	if !h.authorize(c, "get", c.GetString(clusterNameContextKey), wf.Namespace) {
		return
	}
	limit := 20
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	offset := 0
	if v, err := strconv.Atoi(c.Query("offset")); err == nil && v > 0 {
		offset = v
	}
	var runs []store.WorkflowRun
	if err := h.store.DB().Where("workflow_id = ?", wf.ID).
		Order("run_number desc").Limit(limit).Offset(offset).
		Find(&runs).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	names := h.deps.UserNames(triggerUserIDs(runs))
	out := make([]runSummary, 0, len(runs))
	for _, r := range runs {
		out = append(out, toRunSummary(r, names))
	}
	c.JSON(http.StatusOK, gin.H{"runs": out, "total": len(out)})
}

func triggerUserIDs(runs []store.WorkflowRun) []uint {
	ids := make([]uint, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.TriggeredBy)
	}
	return ids
}

func toRunSummary(r store.WorkflowRun, names map[uint]string) runSummary {
	return runSummary{
		ID:              r.ID,
		RunNumber:       r.RunNumber,
		RevisionRev:     r.RevisionRev,
		Status:          r.Status,
		TriggerType:     r.TriggerType,
		TriggeredBy:     r.TriggeredBy,
		TriggeredByName: names[r.TriggeredBy],
		TriggerRef:      r.TriggerRef,
		Params:          json.RawMessage(r.Params),
		StartedAt:       r.StartedAt,
		FinishedAt:      r.FinishedAt,
	}
}

// GetRun returns the run detail: topology from the pinned spec plus every
// NodeRun with status, outputs, error and log tail (design §10).
func (h *Handler) GetRun(c *gin.Context) {
	run, wf, dag, nodes, ok := h.loadRunGraph(c)
	if !ok {
		return
	}
	names := h.deps.UserNames([]uint{run.TriggeredBy})
	type nodeView struct {
		NodeID     string          `json:"nodeId"`
		Type       string          `json:"type"`
		Name       string          `json:"name"`
		Status     string          `json:"status"`
		Inputs     json.RawMessage `json:"inputs"`
		Outputs    json.RawMessage `json:"outputs"`
		Error      string          `json:"error"`
		LogTail    string          `json:"logTail"`
		Attempt    int             `json:"attempt"`
		StartedAt  *time.Time      `json:"startedAt"`
		FinishedAt *time.Time      `json:"finishedAt"`
	}
	byID := map[string]spec.Node{}
	for _, n := range dag.Nodes {
		byID[n.ID] = n
	}
	nodeViews := make([]nodeView, 0, len(nodes))
	for _, n := range nodes {
		sn := byID[n.NodeID]
		nodeViews = append(nodeViews, nodeView{
			NodeID: n.NodeID, Type: sn.Type, Name: sn.Name, Status: n.Status,
			Inputs: json.RawMessage(n.Inputs), Outputs: json.RawMessage(n.Outputs),
			Error: n.Error, LogTail: n.LogTail, Attempt: n.Attempt,
			StartedAt: n.StartedAt, FinishedAt: n.FinishedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"id":                run.ID,
		"runNumber":         run.RunNumber,
		"workflow":          wf.Name,
		"workflowNamespace": wf.Namespace,
		"revisionRev":       run.RevisionRev,
		"status":            run.Status,
		"triggerType":       run.TriggerType,
		"triggeredBy":       run.TriggeredBy,
		"triggeredByName":   names[run.TriggeredBy],
		"triggerRef":        run.TriggerRef,
		"params":            json.RawMessage(run.Params),
		"startedAt":         run.StartedAt,
		"finishedAt":        run.FinishedAt,
		"graph": gin.H{
			"nodes": dag.Nodes,
			"edges": dag.Edges,
		},
		"nodes": nodeViews,
	})
}

// loadRunGraph fetches run + workflow + pinned spec + node rows after the
// get-permission check; ok=false means the response is already written.
func (h *Handler) loadRunGraph(c *gin.Context) (store.WorkflowRun, store.Workflow, spec.DAGSpec, []store.NodeRun, bool) {
	runID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || runID == 0 {
		h.fail(c, http.StatusBadRequest, nil, "invalid run id")
		return store.WorkflowRun{}, store.Workflow{}, spec.DAGSpec{}, nil, false
	}
	var run store.WorkflowRun
	if err := h.store.DB().First(&run, runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.fail(c, http.StatusNotFound, nil, "workflow run not found")
			return store.WorkflowRun{}, store.Workflow{}, spec.DAGSpec{}, nil, false
		}
		h.fail(c, http.StatusInternalServerError, err, "")
		return store.WorkflowRun{}, store.Workflow{}, spec.DAGSpec{}, nil, false
	}
	var wf store.Workflow
	if err := h.store.DB().First(&wf, run.WorkflowID).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return store.WorkflowRun{}, store.Workflow{}, spec.DAGSpec{}, nil, false
	}
	if !h.authorize(c, "get", c.GetString(clusterNameContextKey), wf.Namespace) {
		return store.WorkflowRun{}, store.Workflow{}, spec.DAGSpec{}, nil, false
	}
	var rev store.WorkflowRevision
	if err := h.store.DB().Where("workflow_id = ? AND rev = ?", wf.ID, run.RevisionRev).First(&rev).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return store.WorkflowRun{}, store.Workflow{}, spec.DAGSpec{}, nil, false
	}
	var dag spec.DAGSpec
	if err := rev.Spec.Unmarshal(&dag); err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return store.WorkflowRun{}, store.Workflow{}, spec.DAGSpec{}, nil, false
	}
	var nodes []store.NodeRun
	if err := h.store.DB().Where("run_id = ?", run.ID).Order("id").Find(&nodes).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return store.WorkflowRun{}, store.Workflow{}, spec.DAGSpec{}, nil, false
	}
	return run, wf, dag, nodes, true
}

// CancelRun cancels a queued/running run (exec permission, design §8).
func (h *Handler) CancelRun(c *gin.Context) {
	runID, runNumber, wf, ok := h.runIDAndWorkflow(c)
	if !ok {
		return
	}
	if !h.authorize(c, "exec", c.GetString(clusterNameContextKey), wf.Namespace) {
		return
	}
	if err := h.engine.Cancel(runID); err != nil {
		h.failEngine(c, err)
		return
	}
	h.recordAudit(c, store.HistoryEntry{
		Cluster: c.GetString(clusterNameContextKey), ResourceType: authzResource,
		ResourceName: fmt.Sprintf("%s#%d", wf.Name, runNumber),
		Namespace:    wf.Namespace, Operation: "cancel",
	})
	c.JSON(http.StatusOK, gin.H{"message": "run cancelled"})
}

// RetryRun re-drives a failed/cancelled run from its failed nodes
// (exec permission, design §8).
func (h *Handler) RetryRun(c *gin.Context) {
	runID, runNumber, wf, ok := h.runIDAndWorkflow(c)
	if !ok {
		return
	}
	if !h.authorize(c, "exec", c.GetString(clusterNameContextKey), wf.Namespace) {
		return
	}
	if err := h.engine.Retry(runID); err != nil {
		h.failEngine(c, err)
		return
	}
	h.recordAudit(c, store.HistoryEntry{
		Cluster: c.GetString(clusterNameContextKey), ResourceType: authzResource,
		ResourceName: fmt.Sprintf("%s#%d", wf.Name, runNumber),
		Namespace:    wf.Namespace, Operation: "retry",
	})
	c.JSON(http.StatusOK, gin.H{"message": "run requeued"})
}

// Decide returns the approve/reject handler. The assignees list is itself
// the authorization — the caller must be one of the approval node's
// assignees or an admin; no workflows verb is consulted (design §8).
func (h *Handler) Decide(approve bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		runID, runNumber, wf, ok := h.runIDAndWorkflow(c)
		if !ok {
			return
		}
		var req struct {
			Comment string `json:"comment"`
		}
		if c.Request.ContentLength > 0 {
			if err := c.ShouldBindJSON(&req); err != nil {
				h.fail(c, http.StatusBadRequest, err, "")
				return
			}
		}
		operator := h.deps.OperatorID(c)
		nodeID := c.Param("nodeId")
		if !h.canDecide(c, wf, runID, nodeID, operator) {
			return
		}
		if err := h.engine.Decide(runID, nodeID, approve, req.Comment, operator); err != nil {
			h.failEngine(c, err)
			return
		}
		operation := "approve"
		decision := "approved"
		if !approve {
			operation = "reject"
			decision = "rejected"
		}
		detail, _ := json.Marshal(map[string]any{
			"run": runID, "node": nodeID, "decision": decision, "comment": req.Comment,
		})
		h.recordAudit(c, store.HistoryEntry{
			Cluster: c.GetString(clusterNameContextKey), ResourceType: authzResource,
			ResourceName: fmt.Sprintf("%s#%d", wf.Name, runNumber),
			Namespace:    wf.Namespace, Operation: operation, YAML: string(detail),
		})
		c.JSON(http.StatusOK, gin.H{"message": "approval recorded"})
	}
}

// canDecide checks the approver is an assignee of the node or an admin and
// writes the 403 response otherwise.
func (h *Handler) canDecide(c *gin.Context, wf store.Workflow, runID uint, nodeID string, operator uint) bool {
	if h.deps.IsAdmin(c) {
		return true
	}
	var run store.WorkflowRun
	if err := h.store.DB().Select("revision_rev").First(&run, runID).Error; err != nil {
		h.failEngine(c, engine.ErrNotFound)
		return false
	}
	var rev store.WorkflowRevision
	if err := h.store.DB().Select("spec").
		Where("workflow_id = ? AND rev = ?", wf.ID, run.RevisionRev).
		First(&rev).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return false
	}
	var dag spec.DAGSpec
	if err := rev.Spec.Unmarshal(&dag); err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return false
	}
	for _, n := range dag.Nodes {
		if n.ID != nodeID || n.Type != spec.NodeApproval {
			continue
		}
		var p spec.ApprovalProps
		b, err := json.Marshal(n.Props)
		if err != nil {
			break
		}
		if json.Unmarshal(b, &p) != nil {
			break
		}
		for _, id := range p.Assignees {
			if id == operator {
				return true
			}
		}
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "only assignees or an admin may decide this approval"})
	return false
}

// runIDAndWorkflow resolves the /runs/:id paths to the run id, display
// number and workflow, writing the error response when either is missing.
func (h *Handler) runIDAndWorkflow(c *gin.Context) (uint, int, store.Workflow, bool) {
	runID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || runID == 0 {
		h.fail(c, http.StatusBadRequest, nil, "invalid run id")
		return 0, 0, store.Workflow{}, false
	}
	var run store.WorkflowRun
	if err := h.store.DB().Select("id", "workflow_id", "run_number").First(&run, runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.fail(c, http.StatusNotFound, nil, "workflow run not found")
			return 0, 0, store.Workflow{}, false
		}
		h.fail(c, http.StatusInternalServerError, err, "")
		return 0, 0, store.Workflow{}, false
	}
	var wf store.Workflow
	if err := h.store.DB().First(&wf, run.WorkflowID).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return 0, 0, store.Workflow{}, false
	}
	return uint(runID), run.RunNumber, wf, true
}
