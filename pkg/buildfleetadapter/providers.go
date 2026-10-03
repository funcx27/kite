package buildfleetadapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"k8s.io/klog/v2"

	"github.com/funcx27/kite/modules/buildfleet/config"
	"github.com/zxh326/kite/pkg/model"
	"github.com/zxh326/kite/pkg/rbac"
)

// providerAuthzResource is the pseudo-resource the managed build provider API
// authorizes against: the same judgment the workflow module's build-settings
// API applies (workflows CRUD, target row cluster/namespace), so one role
// shape manages both settings surfaces.
const providerAuthzResource = "workflows"

// kubeconfigRelDir is the cache-relative directory holding the managed
// providers' exported kubeconfigs.
const kubeconfigRelDir = "kite/build-providers"

var dns1123LabelPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// validDNS1123Label reports whether value is a DNS-1123 label (lowercase
// alphanumeric and dashes, 63 chars max).
func validDNS1123Label(value string) bool {
	return len(value) <= 63 && dns1123LabelPattern.MatchString(value)
}

// kubeconfigPath is the managed kubeconfig file of one provider name.
func kubeconfigPath(name string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir: %w", err)
	}
	return filepath.Join(cache, kubeconfigRelDir, name+".kubeconfig"), nil
}

// exportKubeconfig writes the cluster's stored kubeconfig to the provider's
// cache file and returns its path. 0600 (0700 for the directory): the file
// carries cluster credentials and must stay private to the kite user.
func exportKubeconfig(name string, kubeconfig model.SecretString) (string, error) {
	path, err := kubeconfigPath(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create kubeconfig dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		return "", fmt.Errorf("write kubeconfig: %w", err)
	}
	return path, nil
}

// buildProviderConfig validates the row's target cluster (exists, connected,
// kubeconfig-exportable) and returns the fleet provider config pointing at
// the exported kubeconfig. In-cluster installs keep Kubeconfig empty: the
// provider then uses the same ServiceAccount identity as the kite host.
// Out-of-cluster targets get PortForward on: runtime pods are addressed
// through an apiserver tunnel, since the daemon outside the cluster cannot
// resolve in-cluster Service DNS.
func (d *Deps) buildProviderConfig(row model.BuildProvider) (config.Provider, error) {
	pc := config.Provider{Type: "k8s", Name: row.Name, Namespace: row.Namespace}
	if row.CPU > 0 {
		pc.Total.CPU = row.CPU
	}
	if row.Memory != "" {
		pc.Total.Memory = row.Memory
	}
	cl, err := model.GetClusterByName(row.Cluster)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return pc, fmt.Errorf("cluster %q does not exist", row.Cluster)
		}
		return pc, fmt.Errorf("load cluster %q: %w", row.Cluster, err)
	}
	// GetClientSet only resolves connected clusters: the snapshot excludes
	// entries still in the error state, so this doubles as the health check.
	if _, err := d.cm.GetClientSet(row.Cluster); err != nil {
		return pc, fmt.Errorf("cluster %q is not connected: %w", row.Cluster, err)
	}
	if cl.InCluster {
		return pc, nil
	}
	// Agent connections ride an in-process tunnel; no serializable
	// kubeconfig exists for buildkitd provisioning.
	if cl.ClusterAgent {
		return pc, fmt.Errorf("cluster %q is connected through a cluster agent, which build providers cannot use", row.Cluster)
	}
	if cl.Config == "" {
		return pc, fmt.Errorf("cluster %q has no stored kubeconfig to export", row.Cluster)
	}
	path, err := exportKubeconfig(row.Name, cl.Config)
	if err != nil {
		return pc, err
	}
	pc.Kubeconfig = path
	// Unlike config-file providers, API-registered entries skip the
	// providerDefaults merge, so the default must come from here: with the
	// daemon outside the cluster, the runtime address would otherwise be the
	// in-cluster Service DNS name, which the daemon cannot resolve.
	pc.PortForward = true
	return pc, nil
}

// authorizeProvider writes the unified 403 body and returns false when the
// user may not perform verb on the workflows pseudo-resource in the provider
// row's target cluster/namespace — the cluster the provider provisions into
// (kubeconfig export, buildkitd), which must be checked regardless of which
// cluster the request context carries.
func authorizeProvider(c *gin.Context, verb, cluster, namespace string) bool {
	user := c.MustGet("user").(model.User)
	if rbac.CanAccess(user, providerAuthzResource, verb, cluster, namespace) {
		return true
	}
	c.AbortWithStatusJSON(http.StatusForbidden,
		gin.H{"error": rbac.NoAccess(user.Key(), verb, providerAuthzResource, namespace, cluster)})
	return false
}

// RegisterProviderRoutes mounts the managed build provider API under
// /buildfleet/providers. Handlers authorize themselves like the rest of the
// module's non-resource surface.
func (d *Deps) RegisterProviderRoutes(api *gin.RouterGroup) {
	api.GET("/providers", d.handleListProviders)
	api.POST("/providers", d.handleCreateProvider)
	api.DELETE("/providers/:name", d.handleDeleteProvider)
}

type buildProviderJSON struct {
	Name      string  `json:"name"`
	Cluster   string  `json:"cluster"`
	Namespace string  `json:"namespace"`
	CPU       float64 `json:"cpu"`
	Memory    string  `json:"memory"`
}

func toBuildProviderJSON(r model.BuildProvider) buildProviderJSON {
	return buildProviderJSON{
		Name:      r.Name,
		Cluster:   r.Cluster,
		Namespace: r.Namespace,
		CPU:       r.CPU,
		Memory:    r.Memory,
	}
}

// handleListProviders returns the managed providers plus the config-file
// provider names (which are read-only here and rejected at creation).
func (d *Deps) handleListProviders(c *gin.Context) {
	user := c.MustGet("user").(model.User)
	rows, err := model.ListBuildProviders()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Per-row get check on the row's own target cluster/namespace, so rows on
	// clusters the user has no workflows access to stay invisible.
	out := make([]buildProviderJSON, 0, len(rows))
	for i := range rows {
		if !rbac.CanAccess(user, providerAuthzResource, "get", rows[i].Cluster, rows[i].Namespace) {
			continue
		}
		out = append(out, toBuildProviderJSON(rows[i]))
	}
	c.JSON(http.StatusOK, gin.H{"providers": out, "configProviders": d.providerNames})
}

type createBuildProviderRequest struct {
	Name      string  `json:"name" binding:"required"`
	Cluster   string  `json:"cluster" binding:"required"`
	Namespace string  `json:"namespace" binding:"required"`
	CPU       float64 `json:"cpu"`
	Memory    string  `json:"memory"`
}

// handleCreateProvider registers a k8s build provider from a managed cluster:
// validate → export kubeconfig → register with the fleet runtime → persist.
func (d *Deps) handleCreateProvider(c *gin.Context) {
	var req createBuildProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request body: %v", err)})
		return
	}
	row := model.BuildProvider{
		Name:      strings.TrimSpace(req.Name),
		Cluster:   strings.TrimSpace(req.Cluster),
		Namespace: strings.TrimSpace(req.Namespace),
		CPU:       req.CPU,
		Memory:    strings.TrimSpace(req.Memory),
	}
	if !validDNS1123Label(row.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must be a DNS-1123 label (lowercase letters, digits and dashes, 63 chars max)"})
		return
	}
	if !validDNS1123Label(row.Namespace) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "namespace must be a DNS-1123 label (lowercase letters, digits and dashes, 63 chars max)"})
		return
	}
	// The gate targets the row's cluster: this is the cluster whose
	// kubeconfig gets exported and where buildkitd is provisioned.
	if !authorizeProvider(c, "create", row.Cluster, row.Namespace) {
		return
	}
	for _, n := range d.providerNames {
		if n == row.Name {
			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("provider %q is already defined in the buildfleet config", row.Name)})
			return
		}
	}
	if _, err := model.GetBuildProviderByName(row.Name); err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("build provider %q already exists", row.Name)})
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	pc, err := d.buildProviderConfig(row)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := d.app.RegisterK8sProvider(c.Request.Context(), pc); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.AddBuildProvider(&row); err != nil {
		if rmErr := d.app.RemoveProvider(row.Name); rmErr != nil {
			klog.Errorf("failed to unregister build provider %s after failed persist: %v", row.Name, rmErr)
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	recordProviderAudit(c, "create", row)
	c.JSON(http.StatusCreated, toBuildProviderJSON(row))
}

// handleDeleteProvider unregisters and removes one managed provider. Live
// buildkitd instances it owns drain through the ownership map; build
// settings referencing the name fail at run time until reconfigured.
func (d *Deps) handleDeleteProvider(c *gin.Context) {
	name := c.Param("name")
	if !validDNS1123Label(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must be a DNS-1123 label"})
		return
	}
	row, err := model.GetBuildProviderByName(name)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "build provider not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !authorizeProvider(c, "delete", row.Cluster, row.Namespace) {
		return
	}
	if _, err := model.DeleteBuildProvider(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := d.app.RemoveProvider(name); err != nil {
		// The DB row is gone, so the next restart sheds it too; the runtime
		// stays registered until then.
		klog.Errorf("failed to unregister build provider %s: %v", name, err)
	}
	if path, perr := kubeconfigPath(name); perr == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			klog.Errorf("failed to remove kubeconfig of build provider %s: %v", name, err)
		}
	}
	recordProviderAudit(c, "delete", *row)
	c.JSON(http.StatusOK, gin.H{"message": "build provider deleted"})
}

// recordProviderAudit persists one audit row for a provider CRUD change,
// attributed to the operator like the build endpoints.
func recordProviderAudit(c *gin.Context, op string, row model.BuildProvider) {
	user := c.MustGet("user").(model.User)
	histRow := model.ResourceHistory{
		ClusterName:     row.Cluster,
		ResourceType:    "build-provider",
		ResourceName:    row.Name,
		OperationType:   op,
		OperationSource: historySource,
		Success:         true,
		OperatorID:      user.ID,
	}
	if err := model.DB.Create(&histRow).Error; err != nil {
		klog.Errorf("failed to record build provider history: %v", err)
	}
}

// restoreProviders re-registers the managed build providers persisted in the
// database after a process restart. Rows whose cluster went missing or
// unhealthy are skipped with a warning and stay manageable via the API; a
// missing table (first boot: the module migrations run later in route setup)
// simply means there are no rows yet.
func (d *Deps) restoreProviders(ctx context.Context) {
	rows, err := model.ListBuildProviders()
	if err != nil {
		klog.Warningf("build providers not restored: %v", err)
		return
	}
	for i := range rows {
		pc, err := d.buildProviderConfig(rows[i])
		if err != nil {
			klog.Errorf("skipping build provider %s: %v", rows[i].Name, err)
			continue
		}
		if err := d.app.RegisterK8sProvider(ctx, pc); err != nil {
			klog.Errorf("failed to register build provider %s: %v", rows[i].Name, err)
		}
	}
}
