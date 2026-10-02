// Package workflowadapter wires kite capabilities to the workflow module's
// Deps interface. It is the only kite-side package aware of both
// (modules/README.md contract §1: adapter is the single bridge).
package workflowadapter

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"k8s.io/klog/v2"

	"github.com/funcx27/kite/modules/workflow"
	"github.com/funcx27/kite/modules/workflow/exec"
	"github.com/zxh326/kite/pkg/cluster"
	"github.com/zxh326/kite/pkg/model"
	"github.com/zxh326/kite/pkg/rbac"
	"github.com/zxh326/kite/pkg/utils"
)

// historySource marks audit rows written by the workflow module.
const historySource = "workflow"

// encryptionErrorPrefix marks the sentinel string utils.EncryptString returns
// on failure instead of an error (modules/README.md contract §4).
const encryptionErrorPrefix = "encryption_error: "

// notifyTimeout bounds one webhook delivery; a hung receiver must not occupy
// an engine dispatch slot indefinitely.
const notifyTimeout = 10 * time.Second

// Deps implements workflow.Deps on top of kite's RBAC roles, user model and
// ResourceHistory audit log. The M2 executors (buildfleet BuildImage,
// application DeployApp) are not wired yet: they fail nodes with an explicit
// not-wired error while the engine path stays complete.
type Deps struct {
	cm *cluster.ClusterManager
}

var _ workflow.Deps = (*Deps)(nil)

// NewDeps builds the adapter and wires the module's secret codec; it runs at
// route registration, before any module table is read or written.
func NewDeps(cm *cluster.ClusterManager) *Deps {
	d := &Deps{cm: cm}
	workflow.SetSecretCodec(d.EncryptSecret, d.DecryptSecret)
	return d
}

// Migrate creates or updates the workflow module's tables on db.
func Migrate(db *gorm.DB) error {
	return workflow.Migrate(db)
}

func (d *Deps) Authorize(c *gin.Context, resource, verb, cluster, namespace string) bool {
	user := c.MustGet("user").(model.User)
	return rbac.CanAccess(user, resource, verb, cluster, namespace)
}

// AuthorizeUser reports whether the named user may perform verb on resource
// in cluster/namespace — the identity-based check engine node dispatch uses,
// since runs execute outside any request (design §8). The user is loaded
// fresh: CanAccess judges by username/OIDC groups against the current role
// config, and an unknown user is denied.
func (d *Deps) AuthorizeUser(userID uint, resource, verb, cluster, namespace string) bool {
	var user model.User
	if err := model.DB.First(&user, userID).Error; err != nil {
		klog.Errorf("Failed to load user %d for workflow authorization: %v", userID, err)
		return false
	}
	return rbac.CanAccess(user, resource, verb, cluster, namespace)
}

func (d *Deps) OperatorID(c *gin.Context) uint {
	user := c.MustGet("user").(model.User)
	return user.ID
}

// IsAdmin reports whether the request's user holds the host admin role —
// the same judgment as auth.RequireAdmin (design §8: assignees 或 admin).
func (d *Deps) IsAdmin(c *gin.Context) bool {
	user := c.MustGet("user").(model.User)
	return rbac.UserHasRole(user, model.DefaultAdminRole.Name)
}

// UserNames resolves user IDs to their display name (Name, falling back to
// Username); unknown or deleted IDs are absent from the map.
func (d *Deps) UserNames(ids []uint) map[uint]string {
	out := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return out
	}
	var users []model.User
	if err := model.DB.Where("id IN ?", ids).Find(&users).Error; err != nil {
		klog.Errorf("Failed to resolve user names: %v", err)
		return out
	}
	for _, u := range users {
		name := u.Name
		if name == "" {
			name = u.Username
		}
		out[u.ID] = name
	}
	return out
}

func (d *Deps) DB() *gorm.DB {
	return model.DB
}

// EncryptSecret seals plaintext with the host's at-rest encryption
// (utils.EncryptString), the same wire format as model.SecretString.
func (d *Deps) EncryptSecret(plaintext string) (string, error) {
	encrypted := utils.EncryptString(plaintext)
	if detail, ok := strings.CutPrefix(encrypted, encryptionErrorPrefix); ok {
		return "", fmt.Errorf("encrypt secret: %s", detail)
	}
	return encrypted, nil
}

// DecryptSecret opens ciphertext produced by EncryptSecret.
func (d *Deps) DecryptSecret(encrypted string) (string, error) {
	plaintext, err := utils.DecryptString(encrypted)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return plaintext, nil
}

// RecordHistory persists one audit row attributed to the workflow module.
func (d *Deps) RecordHistory(entry workflow.HistoryEntry) {
	if err := model.DB.Create(&model.ResourceHistory{
		ClusterName:     entry.Cluster,
		ResourceType:    entry.ResourceType,
		ResourceName:    entry.ResourceName,
		Namespace:       entry.Namespace,
		OperationType:   entry.Operation,
		OperationSource: historySource,
		ResourceYAML:    entry.YAML,
		PreviousYAML:    entry.PreviousYAML,
		Success:         entry.Success,
		ErrorMessage:    entry.Err,
		OperatorID:      entry.OperatorID,
	}).Error; err != nil {
		// Never log the entry payload: run details may carry parameter values.
		klog.Errorf("Failed to record workflow history: %v", err)
	}
}

// errNotWired marks executors whose backing integration ships in M2
// (design §12): nodes fail with this explicit reason instead of silently
// no-oping.
func errNotWired(capability string) error {
	return fmt.Errorf("%s executor is not wired until workflow M2", capability)
}

// BuildImage is the M2 seam: buildfleet integration lands with the credential
// center (design §5.1/§9).
func (d *Deps) BuildImage(ctx context.Context, req exec.BuildRequest, onLog func(string)) (exec.BuildResult, error) {
	return exec.BuildResult{}, errNotWired("build")
}

// DeployApp is the M2 seam: the application module deploy facade lands there
// (design §5.2).
func (d *Deps) DeployApp(ctx context.Context, req exec.DeployRequest) (exec.DeployResult, error) {
	return exec.DeployResult{}, errNotWired("deploy")
}

// Notify delivers one webhook notification as a JSON POST with a timeout
// (design §5.4). Delivery failures return as errors — notify nodes run with
// continue semantics, so they never block the pipeline.
func (d *Deps) Notify(ctx context.Context, req exec.NotifyRequest) error {
	payload := req.Payload
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("notify: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: notifyTimeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notify: receiver answered %s", resp.Status)
	}
	return nil
}
