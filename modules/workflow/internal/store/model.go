// Package store holds the workflow module's GORM models: the workflow
// definition, its revision chain, runs pinned to a revision and the per-node
// execution rows (design §3).
package store

import "time"

// Model carries the columns shared by the module's mutable tables.
type Model struct {
	ID        uint      `json:"id" gorm:"primarykey"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Workflow lifecycle statuses (design §3/§6).
const (
	RunQueued    = "queued"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

// Node execution statuses (design §3/§6).
const (
	NodePending     = "pending"
	NodeRunning     = "running"
	NodeSucceeded   = "succeeded"
	NodeFailed      = "failed"
	NodeSkipped     = "skipped"
	NodeCancelled   = "cancelled"
	NodeInterrupted = "interrupted"
)

// Run trigger types (design §3).
const (
	TriggerManual  = "manual"
	TriggerWebhook = "webhook"
	TriggerCron    = "cron"
)

// MaxRunsPerWorkflow is the number of runs kept per workflow (with their
// NodeRun rows cascaded away); older rows are pruned by a single DELETE after
// each insert, no background task.
const MaxRunsPerWorkflow = 50

// Workflow is a workflow definition: a globally-unique logical entity whose
// current shape is the revision at CurrentRev.
type Workflow struct {
	Model
	Name        string `json:"name" gorm:"type:varchar(63);uniqueIndex;not null"` // DNS-1123
	Namespace   string `json:"namespace" gorm:"type:varchar(255);not null"`       // owning space, aligned with the application module
	Description string `json:"description" gorm:"type:text"`
	CurrentRev  int    `json:"currentRev" gorm:"not null;default:0"`
	// NextRunNumber is the display sequence of the next run (#1, #2, ...);
	// allocated with an atomic increment inside the trigger transaction.
	NextRunNumber int `json:"-" gorm:"not null;default:1"`
	// WebhookToken authenticates the M3 public webhook; encrypted at rest.
	WebhookToken SecretString `json:"-" gorm:"type:text"`

	// Revisions and Runs associations exist for the FK cascades: deleting a
	// workflow removes its revision chain and run history (design §3).
	Revisions []WorkflowRevision `json:"-" gorm:"foreignKey:WorkflowID;constraint:OnDelete:CASCADE"`
	Runs      []WorkflowRun      `json:"-" gorm:"foreignKey:WorkflowID;constraint:OnDelete:CASCADE"`
}

func (Workflow) TableName() string {
	return "workflows"
}

// WorkflowRevision is one entry of the definition version chain: every save
// appends a row; Rev is monotonic per workflow and never reused. Runs pin the
// Rev they were triggered with, so history stays replayable (design §3).
type WorkflowRevision struct {
	ID         uint      `json:"id" gorm:"primarykey"`
	WorkflowID uint      `json:"workflowId" gorm:"not null;uniqueIndex:idx_workflow_revisions_wf_rev,priority:1"`
	Rev        int       `json:"rev" gorm:"not null;uniqueIndex:idx_workflow_revisions_wf_rev,priority:2"`
	Spec       JSONField `json:"spec" gorm:"type:text"`     // DAGSpec (design §4)
	Params     JSONField `json:"params" gorm:"type:text"`   // []ParamSpec
	Triggers   JSONField `json:"triggers" gorm:"type:text"` // TriggerConfig (design §7)
	OperatorID uint      `json:"operatorId" gorm:"not null;index;default:0"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (WorkflowRevision) TableName() string {
	return "workflow_revisions"
}

// WorkflowRun is one execution, pinned to the definition revision and the
// parameter snapshot it was triggered with (design §3).
type WorkflowRun struct {
	ID uint `json:"id" gorm:"primarykey"`
	// WorkflowID leads the (workflow, run_number) unique index: display
	// numbers repeat across workflows.
	WorkflowID  uint   `json:"workflowId" gorm:"not null;uniqueIndex:idx_workflow_runs_wf_number,priority:1;index"`
	RunNumber   int    `json:"runNumber" gorm:"not null;uniqueIndex:idx_workflow_runs_wf_number,priority:2"`
	RevisionRev int    `json:"revisionRev" gorm:"not null"`
	Status      string `json:"status" gorm:"type:varchar(20);not null;index"`
	TriggerType string `json:"triggerType" gorm:"type:varchar(20);not null;default:'manual'"`
	// TriggeredBy is the run's RBAC identity: every node executes with this
	// user's permissions (design §7); webhook/cron runs record the last
	// editor of the trigger config.
	TriggeredBy uint      `json:"triggeredBy" gorm:"not null;index;default:0"`
	TriggerRef  string    `json:"triggerRef" gorm:"type:varchar(255)"` // git SHA / branch / manual note
	Params      JSONField `json:"params" gorm:"type:text"`             // resolved parameter snapshot, template root

	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`

	// Nodes association exists for the FK cascade: the run's node rows die
	// with it (design §3 retention).
	Nodes []NodeRun `json:"-" gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE"`
	// Workflow association exists for the run→workflow cascade.
	Workflow *Workflow `json:"workflow,omitempty" gorm:"foreignKey:WorkflowID"`
}

func (WorkflowRun) TableName() string {
	return "workflow_runs"
}

// NodeRun is the persisted state machine cell of one DAG node in one run
// (design §3): rows are all created pending at run creation and only their
// status evolves afterwards — the topology is frozen at trigger time.
type NodeRun struct {
	ID     uint   `json:"id" gorm:"primarykey"`
	RunID  uint   `json:"runId" gorm:"not null;uniqueIndex:idx_node_runs_run_id_node,priority:1"`
	NodeID string `json:"nodeId" gorm:"type:varchar(63);not null;uniqueIndex:idx_node_runs_run_id_node,priority:2"`
	Status string `json:"status" gorm:"type:varchar(20);not null;index"`
	// Inputs is the template-rendered effective props; Outputs feeds
	// downstream templates (approval decisions land here too, design §3).
	Inputs  JSONField `json:"inputs" gorm:"type:text"`
	Outputs JSONField `json:"outputs" gorm:"type:text"`
	// Error holds the failure reason, truncated; LogTail the last
	// LogTailMaxBytes of node output (full logs stream over SSE in M2).
	Error   string `json:"error" gorm:"type:text"`
	LogTail string `json:"logTail" gorm:"type:text"`
	// Attempt increments on retry-from-failed.
	Attempt int `json:"attempt" gorm:"not null;default:0"`

	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`

	// Run association exists for the run→nodes cascade.
	Run *WorkflowRun `json:"-" gorm:"foreignKey:RunID"`
}

func (NodeRun) TableName() string {
	return "node_runs"
}

// BuildSetting is the build-cluster default of one namespace: build nodes in
// that namespace resolve their provider to this row when their props leave
// it empty (design §5.1). The empty Namespace is the global default row.
type BuildSetting struct {
	Model
	// Namespace is the owning space; the empty string is the global default
	// row — exactly one such row, enforced by the unique index.
	Namespace string `json:"namespace" gorm:"type:varchar(255);not null;uniqueIndex"`
	Provider  string `json:"provider" gorm:"type:varchar(63);not null;default:''"`
}

func (BuildSetting) TableName() string {
	return "build_settings"
}
