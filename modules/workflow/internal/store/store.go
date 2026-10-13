package store

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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
	// Workflows used to be namespaced with a unique name index; the global
	// ID-identity model dropped both. The legacy cleanup must run before
	// AutoMigrate: the new plain name index generates the same index name, so
	// AutoMigrate skips creating it while the unique one is still there, and
	// the leftover NOT NULL namespace column would reject inserts of new
	// namespace-less rows.
	if err := dropLegacyWorkflowSchema(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(&Workflow{}, &WorkflowRevision{}, &WorkflowRun{}, &NodeRun{}, &BuildSetting{}, &WorkflowBinding{}); err != nil {
		return fmt.Errorf("migrate workflow models: %w", err)
	}
	return nil
}

// ReplaceBindingsTx swaps all bindings of one workflow under the given kind
// for targets (whole-set replace: delete + batch insert). Callers validate
// targets per kind; the slice may be empty to clear the kind.
func ReplaceBindingsTx(tx *gorm.DB, workflowID uint, kind string, targets []string) error {
	if err := tx.Where("workflow_id = ? AND kind = ?", workflowID, kind).
		Delete(&WorkflowBinding{}).Error; err != nil {
		return fmt.Errorf("replace %s bindings: delete: %w", kind, err)
	}
	if len(targets) == 0 {
		return nil
	}
	rows := make([]WorkflowBinding, 0, len(targets))
	for _, target := range targets {
		rows = append(rows, WorkflowBinding{WorkflowID: workflowID, Kind: kind, Target: target})
	}
	if err := tx.Create(&rows).Error; err != nil {
		return fmt.Errorf("replace %s bindings: create: %w", kind, err)
	}
	return nil
}

// LoadBindings reads the targets of one kind for a batch of workflows in a
// single query; the map omits workflows without bindings.
func LoadBindings(db *gorm.DB, workflowIDs []uint, kind string) (map[uint][]string, error) {
	out := make(map[uint][]string, len(workflowIDs))
	if len(workflowIDs) == 0 {
		return out, nil
	}
	var rows []WorkflowBinding
	if err := db.Select("workflow_id", "target").
		Where("workflow_id IN ? AND kind = ?", workflowIDs, kind).
		Order("target").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load %s bindings: %w", kind, err)
	}
	for _, row := range rows {
		out[row.WorkflowID] = append(out[row.WorkflowID], row.Target)
	}
	return out, nil
}

// legacyWorkflowNameIndex is the pre-globalization unique index on
// workflows(name); the regenerated plain index reuses the same name.
const legacyWorkflowNameIndex = "idx_workflows_name"

// dropLegacyWorkflowSchema removes the pre-globalization unique name index
// and namespace column from an existing workflows table. The index is dropped
// only while still unique — after AutoMigrate the replacement plain index
// carries the same name and must survive. Fresh databases have neither and
// skip both steps.
func dropLegacyWorkflowSchema(db *gorm.DB) error {
	if db.Migrator().HasIndex(&Workflow{}, legacyWorkflowNameIndex) {
		unique, err := indexIsUnique(db, &Workflow{}, legacyWorkflowNameIndex)
		if err != nil {
			return fmt.Errorf("migrate workflows: inspect index %s: %w", legacyWorkflowNameIndex, err)
		}
		if unique {
			if err := db.Migrator().DropIndex(&Workflow{}, legacyWorkflowNameIndex); err != nil {
				return fmt.Errorf("migrate workflows: drop index %s: %w", legacyWorkflowNameIndex, err)
			}
		}
	}
	// The model no longer declares the field, so the raw column name is used.
	if db.Migrator().HasColumn(&Workflow{}, "namespace") {
		if err := db.Migrator().DropColumn(&Workflow{}, "namespace"); err != nil {
			return fmt.Errorf("migrate workflows: drop legacy namespace column: %w", err)
		}
	}
	return nil
}

// indexIsUnique reports whether the named index on dst's table carries a
// uniqueness constraint, via the driver's index introspection. The discard
// logger session keeps the sqlite driver's hardwired Debug() call in
// GetIndexes from printing a PRAGMA trace on every boot.
func indexIsUnique(db *gorm.DB, dst any, name string) (bool, error) {
	indexes, err := db.Session(&gorm.Session{Logger: logger.Discard}).Migrator().GetIndexes(dst)
	if err != nil {
		return false, err
	}
	for _, idx := range indexes {
		if idx.Name() == name {
			unique, ok := idx.Unique()
			return unique && ok, nil
		}
	}
	return false, nil
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
