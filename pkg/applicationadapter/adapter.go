// Package applicationadapter wires kite capabilities to the application
// module's Deps interface. It is the only kite-side package aware of both.
package applicationadapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/application"
	"github.com/zxh326/kite/pkg/cluster"
	"github.com/zxh326/kite/pkg/model"
	"github.com/zxh326/kite/pkg/rbac"
	"github.com/zxh326/kite/pkg/utils"
)

// historySource marks audit rows written by the application module.
const historySource = "application"

// encryptionErrorPrefix marks the sentinel string utils.EncryptString returns
// on failure instead of an error.
const encryptionErrorPrefix = "encryption_error: "

// Deps implements application.Deps on top of kite's cluster manager, RBAC
// roles and ResourceHistory audit log.
type Deps struct {
	cm *cluster.ClusterManager
}

var _ application.Deps = (*Deps)(nil)

func NewDeps(cm *cluster.ClusterManager) *Deps {
	d := &Deps{cm: cm}
	// Runs at route registration, before any module table is read or written:
	// SecretJSON's GORM Scan/Value resolve their codec only here.
	application.SetSecretCodec(d.EncryptSecret, d.DecryptSecret)
	return d
}

// Migrate creates or updates the application module's tables on db.
func Migrate(db *gorm.DB) error {
	return application.Migrate(db)
}

func (d *Deps) ResolveCluster(_ context.Context, name string) (client.Client, error) {
	cs, err := d.cm.GetClientSet(name)
	if err != nil {
		return nil, fmt.Errorf("resolve cluster %q: %w", name, err)
	}
	return cs.K8sClient, nil
}

func (d *Deps) Authorize(c *gin.Context, resource, verb, cluster, namespace string) bool {
	user := c.MustGet("user").(model.User)
	return rbac.CanAccess(user, resource, verb, cluster, namespace)
}

func (d *Deps) OperatorID(c *gin.Context) uint {
	user := c.MustGet("user").(model.User)
	return user.ID
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

func (d *Deps) RecordHistory(entry application.HistoryEntry) {
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
		// Never log the entry payload: manifests may carry Secret values.
		klog.Errorf("Failed to record application history: %v", err)
	}
}
