package store

import (
	"fmt"

	"gorm.io/gorm"
)

// Store is the module's handle on the host database.
type Store struct {
	db *gorm.DB
}

// New wraps the host database handle.
func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying database handle.
func (s *Store) DB() *gorm.DB {
	return s.db
}

// Migrate creates or updates the module tables on the host database.
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&Workflow{}, &WorkflowRevision{}, &WorkflowRun{}, &NodeRun{}, &BuildSetting{}); err != nil {
		return fmt.Errorf("migrate workflow models: %w", err)
	}
	return nil
}

// BuildProviderFor resolves the build provider of a namespace: the exact
// namespace row wins over the global default row (empty namespace); ok is
// false when neither exists.
func (s *Store) BuildProviderFor(namespace string) (string, bool, error) {
	var rows []BuildSetting
	if err := s.db.Where("namespace IN ?", []string{namespace, ""}).Find(&rows).Error; err != nil {
		return "", false, fmt.Errorf("resolve build provider for namespace %q: %w", namespace, err)
	}
	for i := range rows {
		if rows[i].Namespace == namespace {
			return rows[i].Provider, true, nil
		}
	}
	for i := range rows {
		if rows[i].Namespace == "" {
			return rows[i].Provider, true, nil
		}
	}
	return "", false, nil
}

// AppendRevisionTx allocates the next per-workflow Rev (MAX+1), inserts the
// snapshot and advances Workflow.CurrentRev, all within the caller's
// transaction so the workflow update and its snapshot stay atomic (design §3:
// 每次保存定义记一条).
func AppendRevisionTx(tx *gorm.DB, workflowID uint, spec, params, triggers []byte, operatorID uint) (int, error) {
	var maxRev int
	if err := tx.Model(&WorkflowRevision{}).
		Where("workflow_id = ?", workflowID).
		Select("COALESCE(MAX(rev), 0)").
		Scan(&maxRev).Error; err != nil {
		return 0, fmt.Errorf("append revision: query max rev: %w", err)
	}
	rev := WorkflowRevision{
		WorkflowID: workflowID,
		Rev:        maxRev + 1,
		Spec:       JSONField(spec),
		Params:     JSONField(params),
		Triggers:   JSONField(triggers),
		OperatorID: operatorID,
	}
	if err := tx.Create(&rev).Error; err != nil {
		return 0, fmt.Errorf("append revision: create: %w", err)
	}
	if err := tx.Model(&Workflow{}).Where("id = ?", workflowID).
		Update("current_rev", rev.Rev).Error; err != nil {
		return 0, fmt.Errorf("append revision: advance current_rev: %w", err)
	}
	return rev.Rev, nil
}

// CreateRunTx allocates the next display run number atomically, inserts the
// run as queued and expands one pending NodeRun per spec node — the run's
// topology freezes at trigger time (design §3). Returns the run id and its
// display number.
func CreateRunTx(tx *gorm.DB, workflowID uint, rev int, params []byte, triggerType, triggerRef string, triggeredBy uint, nodeIDs []string) (uint, int, error) {
	res := tx.Model(&Workflow{}).Where("id = ?", workflowID).
		Update("next_run_number", gorm.Expr("next_run_number + 1"))
	if res.Error != nil {
		return 0, 0, fmt.Errorf("create run: allocate number: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return 0, 0, fmt.Errorf("create run: workflow %d not found", workflowID)
	}
	var next int
	if err := tx.Model(&Workflow{}).Where("id = ?", workflowID).
		Select("next_run_number").Scan(&next).Error; err != nil {
		return 0, 0, fmt.Errorf("create run: read allocated number: %w", err)
	}
	run := WorkflowRun{
		WorkflowID:  workflowID,
		RunNumber:   next - 1,
		RevisionRev: rev,
		Status:      RunQueued,
		TriggerType: triggerType,
		TriggeredBy: triggeredBy,
		TriggerRef:  triggerRef,
		Params:      JSONField(params),
	}
	if err := tx.Create(&run).Error; err != nil {
		return 0, 0, fmt.Errorf("create run: create: %w", err)
	}
	nodes := make([]NodeRun, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		nodes = append(nodes, NodeRun{RunID: run.ID, NodeID: id, Status: NodePending})
	}
	if len(nodes) > 0 {
		if err := tx.Create(&nodes).Error; err != nil {
			return 0, 0, fmt.Errorf("create run: expand nodes: %w", err)
		}
	}
	return run.ID, run.RunNumber, nil
}

// PruneRuns keeps only the newest MaxRunsPerWorkflow runs of a workflow;
// NodeRun rows are removed by the FK cascade. Called once after each insert,
// no background task (design §3 保留策略).
func PruneRuns(db *gorm.DB, workflowID uint, newestRunNumber int) error {
	err := db.Where("workflow_id = ? AND run_number <= ?", workflowID, newestRunNumber-MaxRunsPerWorkflow).
		Delete(&WorkflowRun{}).Error
	if err != nil {
		return fmt.Errorf("prune runs: %w", err)
	}
	return nil
}

// Error handling and log storage caps (design §3: 失败原因/日志尾部截断存储).
const (
	// ErrorMaxBytes caps NodeRun.Error.
	ErrorMaxBytes = 4 * 1024
	// LogTailMaxBytes caps NodeRun.LogTail (design §3: 默认 256KB 封顶).
	LogTailMaxBytes = 256 * 1024
)

// Truncate keeps the last maxBytes bytes of s, realigned to a UTF-8 rune
// boundary.
func Truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := s[len(s)-maxBytes:]
	// Drop continuation bytes (10xxxxxx) so the tail starts on a rune lead byte.
	for len(cut) > 0 && cut[0]&0xC0 == 0x80 {
		cut = cut[1:]
	}
	return cut
}
