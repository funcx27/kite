package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/funcx27/kite/modules/workflow/internal/spec"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// workflowResponse is the API view of a workflow definition. The current
// spec/params/triggers come from the revision at CurrentRev.
type workflowResponse struct {
	ID          uint            `json:"id"`
	Name        string          `json:"name"`
	Namespace   string          `json:"namespace"`
	Description string          `json:"description"`
	CurrentRev  int             `json:"currentRev"`
	Spec        json.RawMessage `json:"spec,omitempty"`
	Params      json.RawMessage `json:"params,omitempty"`
	Triggers    json.RawMessage `json:"triggers,omitempty"`
	// Apps/Clusters aggregate the deploy-app targets of the current spec so
	// the list view can filter by application; absent when there are none.
	Apps      []string  `json:"apps,omitempty"`
	Clusters  []string  `json:"clusters,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type createWorkflowRequest struct {
	Name        string          `json:"name" binding:"required"`
	Namespace   string          `json:"namespace"`
	Description string          `json:"description"`
	Spec        json.RawMessage `json:"spec"`
	Params      json.RawMessage `json:"params"`
	Triggers    json.RawMessage `json:"triggers"`
}

// updateWorkflowRequest replaces the definition under an optimistic lock:
// expectedRev must equal the workflow's current revision, otherwise the
// definition is reported as modified (409, design §10).
type updateWorkflowRequest struct {
	Description string          `json:"description"`
	Spec        json.RawMessage `json:"spec"`
	Params      json.RawMessage `json:"params"`
	Triggers    json.RawMessage `json:"triggers"`
	ExpectedRev *int            `json:"expectedRev"`
}

func (h *Handler) toWorkflowResponse(wf *store.Workflow, withSpec bool) workflowResponse {
	resp := workflowResponse{
		ID:          wf.ID,
		Name:        wf.Name,
		Namespace:   wf.Namespace,
		Description: wf.Description,
		CurrentRev:  wf.CurrentRev,
		CreatedAt:   wf.CreatedAt,
		UpdatedAt:   wf.UpdatedAt,
	}
	if !withSpec {
		return resp
	}
	var rev store.WorkflowRevision
	if err := h.store.DB().Where("workflow_id = ? AND rev = ?", wf.ID, wf.CurrentRev).First(&rev).Error; err != nil {
		return resp
	}
	resp.Spec = json.RawMessage(rev.Spec)
	resp.Params = json.RawMessage(rev.Params)
	resp.Triggers = json.RawMessage(rev.Triggers)
	return resp
}

// ListWorkflows returns the workflows the caller can access in the request's
// current cluster context, filtered per workflow by namespace. Each entry
// carries the deploy-app targets of its current revision for list filtering.
func (h *Handler) ListWorkflows(c *gin.Context) {
	var workflows []store.Workflow
	if err := h.store.DB().Order("name").Find(&workflows).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	cluster := c.GetString(clusterNameContextKey)
	authorized := make([]*store.Workflow, 0, len(workflows))
	for i := range workflows {
		if !h.deps.Authorize(c, authzResource, "get", cluster, workflows[i].Namespace) {
			continue
		}
		authorized = append(authorized, &workflows[i])
	}
	specs := h.currentSpecs(authorized)
	visible := make([]workflowResponse, 0, len(authorized))
	for _, wf := range authorized {
		resp := h.toWorkflowResponse(wf, false)
		resp.Apps, resp.Clusters = extractDeployTargets(specs[wf.ID])
		visible = append(visible, resp)
	}
	c.JSON(http.StatusOK, gin.H{"workflows": visible, "total": len(visible)})
}

// currentSpecs loads the spec of each workflow's current revision in one
// batched query — the list must not issue a query per workflow. Load errors
// and missing revisions degrade to an absent entry: the deploy-target
// aggregation is advisory metadata, mirroring currentSpecJSON.
func (h *Handler) currentSpecs(workflows []*store.Workflow) map[uint][]byte {
	specs := make(map[uint][]byte, len(workflows))
	if len(workflows) == 0 {
		return specs
	}
	ids := make([]uint, 0, len(workflows))
	wantRev := make(map[uint]int, len(workflows))
	revs := make(map[int]struct{})
	for _, wf := range workflows {
		ids = append(ids, wf.ID)
		wantRev[wf.ID] = wf.CurrentRev
		revs[wf.CurrentRev] = struct{}{}
	}
	revList := make([]int, 0, len(revs))
	for rev := range revs {
		revList = append(revList, rev)
	}
	var rows []store.WorkflowRevision
	if err := h.store.DB().Select("workflow_id", "rev", "spec").
		Where("workflow_id IN ? AND rev IN ?", ids, revList).
		Find(&rows).Error; err != nil {
		return specs
	}
	for _, row := range rows {
		if wantRev[row.WorkflowID] == row.Rev {
			specs[row.WorkflowID] = row.Spec
		}
	}
	return specs
}

// extractDeployTargets collects the deduplicated, sorted app and cluster
// targets of a spec's deploy-app nodes. A malformed spec yields empty slices
// rather than failing the list.
func extractDeployTargets(specRaw []byte) (apps, clusters []string) {
	var dag spec.DAGSpec
	if err := json.Unmarshal(specRaw, &dag); err != nil {
		return nil, nil
	}
	appSet := make(map[string]struct{})
	clusterSet := make(map[string]struct{})
	for _, node := range dag.Nodes {
		if node.Type != spec.NodeDeployApp {
			continue
		}
		if app := propsString(node.Props, "app"); app != "" {
			appSet[app] = struct{}{}
		}
		if cluster := propsString(node.Props, "cluster"); cluster != "" {
			clusterSet[cluster] = struct{}{}
		}
	}
	return sortedSetKeys(appSet), sortedSetKeys(clusterSet)
}

// propsString reads a string props entry; non-string values are skipped.
func propsString(props spec.Props, key string) string {
	s, _ := props[key].(string)
	return s
}

func sortedSetKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// decodeDefinition validates the spec/params/triggers triple of a create or
// update request and returns their canonical JSON encodings. Validation
// failures (design §4) come back as one error carrying every problem.
func decodeDefinition(specRaw, paramsRaw, triggersRaw json.RawMessage) ([]byte, []byte, []byte, error) {
	var dag spec.DAGSpec
	if len(specRaw) > 0 {
		if err := json.Unmarshal(specRaw, &dag); err != nil {
			return nil, nil, nil, fmt.Errorf("spec must be a valid DAGSpec JSON object: %w", err)
		}
	}
	var params []spec.ParamSpec
	if len(paramsRaw) > 0 {
		if err := json.Unmarshal(paramsRaw, &params); err != nil {
			return nil, nil, nil, fmt.Errorf("params must be a JSON array of ParamSpec: %w", err)
		}
	}
	var triggers spec.TriggerConfig
	if len(triggersRaw) > 0 {
		if err := json.Unmarshal(triggersRaw, &triggers); err != nil {
			return nil, nil, nil, fmt.Errorf("triggers must be a valid TriggerConfig JSON object: %w", err)
		}
		if triggers.Cron != "" {
			if err := spec.ValidateCron(triggers.Cron); err != nil {
				return nil, nil, nil, fmt.Errorf("triggers.cron: %w", err)
			}
		}
	}
	if problems := spec.Validate(dag, params); len(problems) > 0 {
		return nil, nil, nil, &validationProblems{Problems: problems}
	}
	specJSON, err := json.Marshal(dag)
	if err != nil {
		return nil, nil, nil, err
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, nil, nil, err
	}
	triggersJSON, err := json.Marshal(triggers)
	if err != nil {
		return nil, nil, nil, err
	}
	return specJSON, paramsJSON, triggersJSON, nil
}

type validationProblems struct{ Problems []string }

func (v *validationProblems) Error() string {
	out := ""
	for i, p := range v.Problems {
		if i > 0 {
			out += "; "
		}
		out += p
	}
	return out
}

// CreateWorkflow validates the definition, creates the workflow with a fresh
// webhook token and appends revision 1 (design §10).
func (h *Handler) CreateWorkflow(c *gin.Context) {
	var req createWorkflowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	if !spec.ValidateDNS1123Label(req.Name, 63) {
		h.fail(c, http.StatusBadRequest, nil, "name must be a DNS-1123 label of at most 63 characters")
		return
	}
	if req.Namespace == "" {
		req.Namespace = "default"
	}
	if !spec.ValidateDNS1123Label(req.Namespace, 63) {
		h.fail(c, http.StatusBadRequest, nil, "namespace must be a DNS-1123 label")
		return
	}
	cluster := c.GetString(clusterNameContextKey)
	if !h.authorize(c, "create", cluster, req.Namespace) {
		return
	}
	specJSON, paramsJSON, triggersJSON, err := decodeDefinition(req.Spec, req.Params, req.Triggers)
	if err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	db := h.store.DB()
	var count int64
	if err := db.Model(&store.Workflow{}).Where("name = ?", req.Name).Count(&count).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if count > 0 {
		h.fail(c, http.StatusConflict, nil, "workflow already exists")
		return
	}
	token, err := generateToken()
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	wf := store.Workflow{Name: req.Name, Namespace: req.Namespace, Description: req.Description, WebhookToken: store.SecretString(token)}
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&wf).Error; err != nil {
			return err
		}
		rev, err := store.AppendRevisionTx(tx, wf.ID, specJSON, paramsJSON, triggersJSON, h.deps.OperatorID(c))
		if err != nil {
			return err
		}
		wf.CurrentRev = rev
		return nil
	})
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	h.recordAudit(c, store.HistoryEntry{
		Cluster: cluster, ResourceType: authzResource, ResourceName: wf.Name,
		Namespace: wf.Namespace, Operation: "create", YAML: string(specJSON),
	})
	c.JSON(http.StatusCreated, h.toWorkflowResponse(&wf, true))
}

// webhookTokenHexLen is the encoded length of the 128-bit hex webhook tokens
// generateToken issues; the public hook endpoint prefilters on it so junk
// requests never reach the token scan.
const webhookTokenHexLen = 32

// generateToken produces a 128-bit random hex webhook token.
func generateToken() (string, error) {
	b := make([]byte, webhookTokenHexLen/2)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate webhook token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// GetWorkflow returns the definition with its current revision content.
func (h *Handler) GetWorkflow(c *gin.Context) {
	wf, ok := h.findWorkflow(c)
	if !ok {
		return
	}
	if !h.authorize(c, "get", c.GetString(clusterNameContextKey), wf.Namespace) {
		return
	}
	c.JSON(http.StatusOK, h.toWorkflowResponse(wf, true))
}

// UpdateWorkflow replaces the definition under the expectedRev optimistic
// lock: the lock re-check, the description update and the appended revision
// run in one transaction, so concurrent saves can neither lose an update nor
// leave the chain inconsistent (design §10).
func (h *Handler) UpdateWorkflow(c *gin.Context) {
	wf, ok := h.findWorkflow(c)
	if !ok {
		return
	}
	if !h.authorize(c, "update", c.GetString(clusterNameContextKey), wf.Namespace) {
		return
	}
	var req updateWorkflowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	if req.ExpectedRev == nil {
		h.fail(c, http.StatusBadRequest, nil, "expectedRev is required")
		return
	}
	specJSON, paramsJSON, triggersJSON, err := decodeDefinition(req.Spec, req.Params, req.Triggers)
	if err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	var newRev int
	err = h.store.DB().Transaction(func(tx *gorm.DB) error {
		var cur store.Workflow
		q := tx.Where("id = ?", wf.ID)
		if tx.Name() != "sqlite" {
			q = q.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := q.First(&cur).Error; err != nil {
			return err
		}
		if cur.CurrentRev != *req.ExpectedRev {
			return errWorkflowModified
		}
		rev, err := store.AppendRevisionTx(tx, wf.ID, specJSON, paramsJSON, triggersJSON, h.deps.OperatorID(c))
		if err != nil {
			return err
		}
		newRev = rev
		return tx.Model(&store.Workflow{}).Where("id = ?", wf.ID).
			Update("description", req.Description).Error
	})
	if errors.Is(err, errWorkflowModified) {
		h.fail(c, http.StatusConflict, nil, "workflow was modified, expectedRev mismatch")
		return
	}
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	previous := h.currentSpecJSON(wf.ID, wf.CurrentRev)
	wf.CurrentRev = newRev
	wf.Description = req.Description
	h.recordAudit(c, store.HistoryEntry{
		Cluster: c.GetString(clusterNameContextKey), ResourceType: authzResource, ResourceName: wf.Name,
		Namespace: wf.Namespace, Operation: "update",
		YAML:         string(specJSON),
		PreviousYAML: previous,
	})
	c.JSON(http.StatusOK, h.toWorkflowResponse(wf, true))
}

// currentSpecJSON reads the spec snapshot of a revision for the audit trail;
// failures degrade to an empty string rather than failing the request.
func (h *Handler) currentSpecJSON(workflowID uint, rev int) string {
	var r store.WorkflowRevision
	if err := h.store.DB().Select("spec").Where("workflow_id = ? AND rev = ?", workflowID, rev).First(&r).Error; err != nil {
		return ""
	}
	return string(r.Spec)
}

// DeleteWorkflow removes the definition and cascades its revision chain and
// run history; deletion is refused while any run is still queued/running
// (409, design §10).
func (h *Handler) DeleteWorkflow(c *gin.Context) {
	wf, ok := h.findWorkflow(c)
	if !ok {
		return
	}
	if !h.authorize(c, "delete", c.GetString(clusterNameContextKey), wf.Namespace) {
		return
	}
	var active int64
	if err := h.store.DB().Model(&store.WorkflowRun{}).
		Where("workflow_id = ? AND status IN ?", wf.ID, []string{store.RunQueued, store.RunRunning}).
		Count(&active).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if active > 0 {
		h.fail(c, http.StatusConflict, nil, "workflow has running runs, cancel them first")
		return
	}
	// Snapshot the definition before the delete: the revision rows cascade
	// away with the workflow, and the audit entry must keep the content of
	// what was deleted.
	specSnapshot := h.currentSpecJSON(wf.ID, wf.CurrentRev)
	if err := h.store.DB().Delete(wf).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	h.recordAudit(c, store.HistoryEntry{
		Cluster: c.GetString(clusterNameContextKey), ResourceType: authzResource, ResourceName: wf.Name,
		Namespace: wf.Namespace, Operation: "delete",
		YAML: specSnapshot,
	})
	c.JSON(http.StatusOK, gin.H{"message": "workflow deleted"})
}

// ListRevisions returns the definition version chain, newest first
// (design §10).
func (h *Handler) ListRevisions(c *gin.Context) {
	wf, ok := h.findWorkflow(c)
	if !ok {
		return
	}
	if !h.authorize(c, "get", c.GetString(clusterNameContextKey), wf.Namespace) {
		return
	}
	var revs []store.WorkflowRevision
	if err := h.store.DB().
		Where("workflow_id = ?", wf.ID).
		Order("rev desc").Find(&revs).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	type revisionSummary struct {
		Rev        int       `json:"rev"`
		OperatorID uint      `json:"operatorId"`
		CreatedAt  time.Time `json:"createdAt"`
	}
	out := make([]revisionSummary, 0, len(revs))
	for _, r := range revs {
		out = append(out, revisionSummary{Rev: r.Rev, OperatorID: r.OperatorID, CreatedAt: r.CreatedAt})
	}
	c.JSON(http.StatusOK, gin.H{"revisions": out})
}

// GetRevision returns one revision's full definition (design §10).
func (h *Handler) GetRevision(c *gin.Context) {
	wf, ok := h.findWorkflow(c)
	if !ok {
		return
	}
	if !h.authorize(c, "get", c.GetString(clusterNameContextKey), wf.Namespace) {
		return
	}
	rev, err := strconv.Atoi(c.Param("rev"))
	if err != nil || rev < 1 {
		h.fail(c, http.StatusBadRequest, nil, "rev must be a positive integer")
		return
	}
	var r store.WorkflowRevision
	if err := h.store.DB().Where("workflow_id = ? AND rev = ?", wf.ID, rev).First(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.fail(c, http.StatusNotFound, nil, "revision not found")
			return
		}
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"rev":        r.Rev,
		"spec":       json.RawMessage(r.Spec),
		"params":     json.RawMessage(r.Params),
		"triggers":   json.RawMessage(r.Triggers),
		"operatorId": r.OperatorID,
		"createdAt":  r.CreatedAt,
	})
}

// GetWebhookToken returns the workflow's webhook token (update permission,
// design §10); the value is generated at workflow creation and rotatable via
// the reset endpoint.
func (h *Handler) GetWebhookToken(c *gin.Context) {
	wf, ok := h.findWorkflow(c)
	if !ok {
		return
	}
	if !h.authorize(c, "update", c.GetString(clusterNameContextKey), wf.Namespace) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": string(wf.WebhookToken)})
}

// ResetWebhookToken replaces the webhook token, invalidating the old value
// (design §10).
func (h *Handler) ResetWebhookToken(c *gin.Context) {
	wf, ok := h.findWorkflow(c)
	if !ok {
		return
	}
	if !h.authorize(c, "update", c.GetString(clusterNameContextKey), wf.Namespace) {
		return
	}
	token, err := generateToken()
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if err := h.store.DB().Model(&store.Workflow{}).Where("id = ?", wf.ID).
		Update("webhook_token", store.SecretString(token)).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	h.recordAudit(c, store.HistoryEntry{
		Cluster: c.GetString(clusterNameContextKey), ResourceType: authzResource, ResourceName: wf.Name,
		Namespace: wf.Namespace, Operation: "reset-webhook-token",
	})
	c.JSON(http.StatusOK, gin.H{"token": token})
}

// errWorkflowModified marks the optimistic-lock mismatch in UpdateWorkflow.
var errWorkflowModified = errors.New("workflow was modified")
