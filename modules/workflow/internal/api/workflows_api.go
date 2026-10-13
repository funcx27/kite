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
	"strings"
	"time"
	"unicode"

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
	Description string          `json:"description"`
	CurrentRev  int             `json:"currentRev"`
	Spec        json.RawMessage `json:"spec,omitempty"`
	Params      json.RawMessage `json:"params,omitempty"`
	Triggers    json.RawMessage `json:"triggers,omitempty"`
	// Apps/Clusters aggregate the deploy-app targets of the current spec so
	// the list view can filter by application; apps are "space/name"
	// composites. Absent when there are none.
	Apps []string `json:"apps,omitempty"`
	// Clusters are the deploy-app cluster targets of the current spec.
	Clusters []string `json:"clusters,omitempty"`
	// Spaces are the bound spaces (visibility metadata); deploy-referenced
	// spaces are derivable from Apps. Absent when nothing is bound.
	Spaces    []string  `json:"spaces,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type createWorkflowRequest struct {
	Name        string          `json:"name" binding:"required"`
	Description string          `json:"description"`
	Spec        json.RawMessage `json:"spec"`
	Params      json.RawMessage `json:"params"`
	Triggers    json.RawMessage `json:"triggers"`
	// Spaces optionally binds the workflow to spaces for list visibility;
	// omitted or empty leaves it platform-level.
	Spaces []string `json:"spaces"`
}

// updateWorkflowRequest replaces the definition under an optimistic lock:
// expectedRev must equal the workflow's current revision, otherwise the
// definition is reported as modified (409, design §10). Absent fields keep
// their stored values — only what the request carries is replaced.
type updateWorkflowRequest struct {
	// Description clears when explicitly set to ""; nil keeps the stored one.
	Description *string         `json:"description"`
	Spec        json.RawMessage `json:"spec"`
	Params      json.RawMessage `json:"params"`
	Triggers    json.RawMessage `json:"triggers"`
	ExpectedRev *int            `json:"expectedRev"`
	// Name renames the workflow when present; nil keeps the stored name.
	Name *string `json:"name"`
	// Spaces replaces the space bindings when present (an empty array clears
	// them); nil leaves them untouched.
	Spaces *[]string `json:"spaces"`
}

// validateWorkflowName guards a display-only label: any unicode is allowed
// (Chinese included), but the value must be trimmed, 1-63 characters and
// free of control characters so it stays canonical and renderable.
func validateWorkflowName(name string) error {
	if name == "" || name != strings.TrimSpace(name) {
		return fmt.Errorf("name must be 1-63 characters with no leading or trailing spaces")
	}
	if len([]rune(name)) > 63 {
		return fmt.Errorf("name must be at most 63 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("name must not contain control characters")
		}
	}
	return nil
}

// normalizeSpaces validates and canonicalizes a space-binding list: every
// entry must be a DNS-1123 label; the result is deduplicated and sorted.
func normalizeSpaces(spaces []string) ([]string, error) {
	seen := make(map[string]struct{}, len(spaces))
	out := make([]string, 0, len(spaces))
	for _, space := range spaces {
		if !spec.ValidateDNS1123Label(space, 63) {
			return nil, fmt.Errorf("space %q must be a DNS-1123 label of at most 63 characters", space)
		}
		if _, dup := seen[space]; dup {
			continue
		}
		seen[space] = struct{}{}
		out = append(out, space)
	}
	sort.Strings(out)
	return out, nil
}

func (h *Handler) toWorkflowResponse(wf *store.Workflow, withSpec bool) workflowResponse {
	resp := workflowResponse{
		ID:          wf.ID,
		Name:        wf.Name,
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

// ListWorkflows returns every workflow to callers holding a wildcard-cluster
// get grant on the pseudo-resource; definitions are global, so the entry check
// replaces the former per-namespace filtering. Each entry carries the
// deploy-app targets of its current revision for list filtering.
func (h *Handler) ListWorkflows(c *gin.Context) {
	if !h.authorize(c, "get", c.GetString(clusterNameContextKey), "") {
		return
	}
	var workflows []store.Workflow
	if err := h.store.DB().Order("name").Find(&workflows).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	all := make([]*store.Workflow, 0, len(workflows))
	for i := range workflows {
		all = append(all, &workflows[i])
	}
	specs := h.currentSpecs(all)
	spaceBindings, err := store.LoadBindings(h.store.DB(), workflowIDs(all), store.BindingKindSpace)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	visible := make([]workflowResponse, 0, len(all))
	for _, wf := range all {
		resp := h.toWorkflowResponse(wf, false)
		resp.Apps, resp.Clusters = extractDeployTargets(specs[wf.ID])
		resp.Spaces = spaceBindings[wf.ID]
		visible = append(visible, resp)
	}
	c.JSON(http.StatusOK, gin.H{"workflows": visible, "total": len(visible)})
}

// workflowIDs collects the IDs of a workflow batch for binding lookups.
func workflowIDs(workflows []*store.Workflow) []uint {
	ids := make([]uint, 0, len(workflows))
	for _, wf := range workflows {
		ids = append(ids, wf.ID)
	}
	return ids
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
// targets of a spec's deploy-app nodes; apps aggregate as "space/name"
// composites because application identity is the (space, name) pair. Targets
// missing either half cannot deploy and are skipped. A malformed spec yields
// empty slices rather than failing the list.
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
		space := propsString(node.Props, "space")
		app := propsString(node.Props, "app")
		if space != "" && app != "" {
			appSet[space+"/"+app] = struct{}{}
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
// webhook token and appends revision 1 (design §10). Names are reusable
// display labels, so there is no uniqueness check; the ID disambiguates.
func (h *Handler) CreateWorkflow(c *gin.Context) {
	var req createWorkflowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	if err := validateWorkflowName(req.Name); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	cluster := c.GetString(clusterNameContextKey)
	if !h.authorize(c, "create", cluster, "") {
		return
	}
	specJSON, paramsJSON, triggersJSON, err := decodeDefinition(req.Spec, req.Params, req.Triggers)
	if err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	spaces, err := normalizeSpaces(req.Spaces)
	if err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	db := h.store.DB()
	token, err := generateToken()
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	wf := store.Workflow{Name: req.Name, Description: req.Description, WebhookToken: store.SecretString(token)}
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&wf).Error; err != nil {
			return err
		}
		rev, err := store.AppendRevisionTx(tx, wf.ID, specJSON, paramsJSON, triggersJSON, h.deps.OperatorID(c))
		if err != nil {
			return err
		}
		wf.CurrentRev = rev
		return store.ReplaceBindingsTx(tx, wf.ID, store.BindingKindSpace, spaces)
	})
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	h.recordAudit(c, store.HistoryEntry{
		Cluster: cluster, ResourceType: authzResource, ResourceName: workflowAuditName(&wf),
		Operation: "create", YAML: string(specJSON),
	})
	resp := h.toWorkflowResponse(&wf, true)
	resp.Spaces = spaces
	c.JSON(http.StatusCreated, resp)
}

// workflowAuditName disambiguates reusable workflow names in audit entries as
// name#id, mirroring the run entries' name#runNumber pattern.
func workflowAuditName(wf *store.Workflow) string {
	return fmt.Sprintf("%s#%d", wf.Name, wf.ID)
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
	if !h.authorize(c, "get", c.GetString(clusterNameContextKey), "") {
		return
	}
	resp := h.toWorkflowResponse(wf, true)
	spaces, err := store.LoadBindings(h.store.DB(), []uint{wf.ID}, store.BindingKindSpace)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	resp.Spaces = spaces[wf.ID]
	c.JSON(http.StatusOK, resp)
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
	if !h.authorize(c, "update", c.GetString(clusterNameContextKey), "") {
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
	// A partial update (e.g. a rename) must not wipe the stored definition:
	// absent spec/params/triggers fall back to the current revision's values
	// before the triple is validated and appended. wf.CurrentRev pairs with
	// the expectedRev check below — a concurrent save bumps it and fails
	// this update with 409 before anything is written.
	if len(req.Spec) == 0 || len(req.Params) == 0 || len(req.Triggers) == 0 {
		var cur store.WorkflowRevision
		if err := h.store.DB().
			Where("workflow_id = ? AND rev = ?", wf.ID, wf.CurrentRev).
			First(&cur).Error; err != nil {
			h.fail(c, http.StatusInternalServerError, err, "")
			return
		}
		if len(req.Spec) == 0 {
			req.Spec = json.RawMessage(cur.Spec)
		}
		if len(req.Params) == 0 {
			req.Params = json.RawMessage(cur.Params)
		}
		if len(req.Triggers) == 0 {
			req.Triggers = json.RawMessage(cur.Triggers)
		}
	}
	specJSON, paramsJSON, triggersJSON, err := decodeDefinition(req.Spec, req.Params, req.Triggers)
	if err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	var spaces []string
	if req.Spaces != nil {
		if spaces, err = normalizeSpaces(*req.Spaces); err != nil {
			h.fail(c, http.StatusBadRequest, err, "")
			return
		}
	}
	if req.Name != nil {
		if err := validateWorkflowName(*req.Name); err != nil {
			h.fail(c, http.StatusBadRequest, err, "")
			return
		}
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
		if req.Description != nil {
			if err := tx.Model(&store.Workflow{}).Where("id = ?", wf.ID).
				Update("description", *req.Description).Error; err != nil {
				return err
			}
		}
		if req.Name != nil {
			if err := tx.Model(&store.Workflow{}).Where("id = ?", wf.ID).
				Update("name", *req.Name).Error; err != nil {
				return err
			}
		}
		if req.Spaces != nil {
			return store.ReplaceBindingsTx(tx, wf.ID, store.BindingKindSpace, spaces)
		}
		return nil
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
	if req.Description != nil {
		wf.Description = *req.Description
	}
	if req.Name != nil {
		wf.Name = *req.Name
	}
	h.recordAudit(c, store.HistoryEntry{
		Cluster: c.GetString(clusterNameContextKey), ResourceType: authzResource, ResourceName: workflowAuditName(wf),
		Operation:    "update",
		YAML:         string(specJSON),
		PreviousYAML: previous,
	})
	resp := h.toWorkflowResponse(wf, true)
	if req.Spaces != nil {
		resp.Spaces = spaces
	} else if bound, err := store.LoadBindings(h.store.DB(), []uint{wf.ID}, store.BindingKindSpace); err == nil {
		resp.Spaces = bound[wf.ID]
	}
	c.JSON(http.StatusOK, resp)
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
	if !h.authorize(c, "delete", c.GetString(clusterNameContextKey), "") {
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
		Cluster: c.GetString(clusterNameContextKey), ResourceType: authzResource, ResourceName: workflowAuditName(wf),
		Operation: "delete",
		YAML:      specSnapshot,
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
	if !h.authorize(c, "get", c.GetString(clusterNameContextKey), "") {
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
	if !h.authorize(c, "get", c.GetString(clusterNameContextKey), "") {
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
	if !h.authorize(c, "update", c.GetString(clusterNameContextKey), "") {
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
	if !h.authorize(c, "update", c.GetString(clusterNameContextKey), "") {
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
		Cluster: c.GetString(clusterNameContextKey), ResourceType: authzResource, ResourceName: workflowAuditName(wf),
		Operation: "reset-webhook-token",
	})
	c.JSON(http.StatusOK, gin.H{"token": token})
}

// errWorkflowModified marks the optimistic-lock mismatch in UpdateWorkflow.
var errWorkflowModified = errors.New("workflow was modified")
