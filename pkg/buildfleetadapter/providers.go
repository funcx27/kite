package buildfleetadapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"k8s.io/klog/v2"

	"github.com/funcx27/kite/modules/buildfleet/config"
	"github.com/zxh326/kite/pkg/cluster"
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
	// Empty Runtime means the "default" profile (config.Profile falls back).
	pc.Runtime = row.Runtime
	if row.ReadyTimeoutSeconds > 0 {
		pc.ReadyTimeout = (time.Duration(row.ReadyTimeoutSeconds) * time.Second).String()
	}
	// API-registered providers skip the config file's providerDefaults merge,
	// so an enabled cache must carry every field complete — the module fails
	// loudly on empty image/size/gcSchedule.
	if row.Cache != nil && row.Cache.Enabled {
		pc.Cache = &config.Cache{
			Enabled:      true,
			Image:        row.Cache.Image,
			StorageClass: row.Cache.StorageClass,
			Size:         row.Cache.Size,
			GCSchedule:   row.Cache.GCSchedule,
		}
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
// /buildfleet/providers plus the build runtime settings surface. Handlers
// authorize themselves like the rest of the module's non-resource surface.
func (d *Deps) RegisterProviderRoutes(api *gin.RouterGroup) {
	api.GET("/providers", d.handleListProviders)
	api.POST("/providers", d.handleCreateProvider)
	api.PUT("/providers/:name", d.handleUpdateProvider)
	api.DELETE("/providers/:name", d.handleDeleteProvider)
	api.GET("/runtime-settings", d.handleGetRuntimeSettings)
	api.PUT("/runtime-settings", d.handleUpdateRuntimeSettings)
}

type buildProviderCacheJSON struct {
	Enabled      bool   `json:"enabled"`
	Image        string `json:"image"`
	StorageClass string `json:"storageClass"`
	Size         string `json:"size"`
	GCSchedule   string `json:"gcSchedule"`
}

type buildProviderJSON struct {
	Name                string                  `json:"name"`
	Cluster             string                  `json:"cluster"`
	Namespace           string                  `json:"namespace"`
	CPU                 float64                 `json:"cpu"`
	Memory              string                  `json:"memory"`
	Runtime             string                  `json:"runtime"`
	ReadyTimeoutSeconds int                     `json:"readyTimeoutSeconds"`
	PodCPU              float64                 `json:"podCpu"`
	PodMemoryGi         float64                 `json:"podMemoryGi"`
	Cache               *buildProviderCacheJSON `json:"cache"`
}

func toBuildProviderJSON(r model.BuildProvider) buildProviderJSON {
	out := buildProviderJSON{
		Name:                r.Name,
		Cluster:             r.Cluster,
		Namespace:           r.Namespace,
		CPU:                 r.CPU,
		Memory:              r.Memory,
		Runtime:             r.Runtime,
		ReadyTimeoutSeconds: r.ReadyTimeoutSeconds,
		PodCPU:              r.PodCPU,
		PodMemoryGi:         r.PodMemoryGi,
	}
	if r.Cache != nil {
		out.Cache = &buildProviderCacheJSON{
			Enabled:      r.Cache.Enabled,
			Image:        r.Cache.Image,
			StorageClass: r.Cache.StorageClass,
			Size:         r.Cache.Size,
			GCSchedule:   r.Cache.GCSchedule,
		}
	}
	return out
}

// runtimeNames lists the config's runtime profile names sorted; "default"
// always exists (the loader merges the compat alias into it).
func (d *Deps) runtimeNames() []string {
	profiles := d.cfg.RuntimeProfiles()
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
	c.JSON(http.StatusOK, gin.H{"providers": out, "configProviders": d.providerNames, "runtimes": d.runtimeNames()})
}

// buildProviderFields carries the request fields shared by create and update.
type buildProviderFields struct {
	Runtime             string                  `json:"runtime"`
	ReadyTimeoutSeconds int                     `json:"readyTimeoutSeconds"`
	PodCPU              float64                 `json:"podCpu"`
	PodMemoryGi         float64                 `json:"podMemoryGi"`
	Cache               *buildProviderCacheJSON `json:"cache"`
}

// applyBuildProviderFields copies the shared fields onto row and validates
// them: the runtime profile must exist, numbers must be non-negative, and an
// enabled cache needs image/size/gcSchedule (storageClass is optional) —
// managed providers get no config-file defaults.
func (d *Deps) applyBuildProviderFields(row *model.BuildProvider, f buildProviderFields) error {
	if f.ReadyTimeoutSeconds < 0 {
		return fmt.Errorf("readyTimeoutSeconds must be >= 0")
	}
	if f.PodCPU < 0 {
		return fmt.Errorf("podCpu must be >= 0")
	}
	if f.PodMemoryGi < 0 {
		return fmt.Errorf("podMemoryGi must be >= 0")
	}
	runtimeName := strings.TrimSpace(f.Runtime)
	if runtimeName != "" && !slices.Contains(d.runtimeNames(), runtimeName) {
		return fmt.Errorf("runtime %q is not a configured runtime profile", runtimeName)
	}
	row.Runtime = runtimeName
	row.ReadyTimeoutSeconds = f.ReadyTimeoutSeconds
	row.PodCPU = f.PodCPU
	row.PodMemoryGi = f.PodMemoryGi
	if f.Cache != nil && f.Cache.Enabled {
		cache := f.Cache
		if strings.TrimSpace(cache.Image) == "" || strings.TrimSpace(cache.Size) == "" || strings.TrimSpace(cache.GCSchedule) == "" {
			return fmt.Errorf("cache requires image, size and gcSchedule (managed providers get no config defaults)")
		}
		row.Cache = &model.BuildProviderCache{
			Enabled:      true,
			Image:        strings.TrimSpace(cache.Image),
			StorageClass: strings.TrimSpace(cache.StorageClass),
			Size:         strings.TrimSpace(cache.Size),
			GCSchedule:   strings.TrimSpace(cache.GCSchedule),
		}
	} else {
		row.Cache = nil
	}
	return nil
}

type createBuildProviderRequest struct {
	Name      string  `json:"name" binding:"required"`
	Cluster   string  `json:"cluster" binding:"required"`
	Namespace string  `json:"namespace" binding:"required"`
	CPU       float64 `json:"cpu"`
	Memory    string  `json:"memory"`
	buildProviderFields
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
	if err := d.applyBuildProviderFields(&row, req.buildProviderFields); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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

// updateBuildProviderRequest is the PUT body; the name rides the path and is
// immutable — renaming means recreating, since workflows pin providers by
// name and the kubeconfig cache file is keyed by it.
type updateBuildProviderRequest struct {
	Cluster   string  `json:"cluster" binding:"required"`
	Namespace string  `json:"namespace" binding:"required"`
	CPU       float64 `json:"cpu"`
	Memory    string  `json:"memory"`
	buildProviderFields
}

// handleUpdateProvider revalidates the row like creation, swaps the runtime
// registration and persists the new values. The name is immutable; the swap
// is remove-then-register, so a failed register leaves the provider
// unregistered until the next restart restores it from the (still old) row.
func (d *Deps) handleUpdateProvider(c *gin.Context) {
	name := c.Param("name")
	if !validDNS1123Label(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must be a DNS-1123 label"})
		return
	}
	var req updateBuildProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request body: %v", err)})
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
	// Mutate the loaded row in place so Save keeps its primary key — a fresh
	// struct would insert and hit the name unique index.
	oldRow := *row
	row.Cluster = strings.TrimSpace(req.Cluster)
	row.Namespace = strings.TrimSpace(req.Namespace)
	row.CPU = req.CPU
	row.Memory = strings.TrimSpace(req.Memory)
	if !validDNS1123Label(row.Namespace) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "namespace must be a DNS-1123 label (lowercase letters, digits and dashes, 63 chars max)"})
		return
	}
	if err := d.applyBuildProviderFields(row, req.buildProviderFields); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !authorizeProvider(c, "update", row.Cluster, row.Namespace) {
		return
	}
	newPC, err := d.buildProviderConfig(*row)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	oldPC, pcErr := d.buildProviderConfig(oldRow)
	// Swap the runtime registration first: a persist failure below can put
	// the old shape back, while the reverse order would leave the runtime
	// serving a shape the row no longer describes.
	if err := d.app.RemoveProvider(name); err != nil {
		// Unregistered runtime (restore previously skipped it) — the register
		// below still records the new shape.
		klog.Warningf("build provider %s was not registered before update: %v", name, err)
	}
	if err := d.app.RegisterK8sProvider(c.Request.Context(), newPC); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.UpdateBuildProvider(row); err != nil {
		if rmErr := d.app.RemoveProvider(name); rmErr != nil {
			klog.Errorf("failed to unregister build provider %s after failed persist: %v", name, rmErr)
		}
		if pcErr == nil {
			if reErr := d.app.RegisterK8sProvider(c.Request.Context(), oldPC); reErr != nil {
				klog.Errorf("failed to re-register old shape of build provider %s: %v", name, reErr)
			}
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	recordProviderAudit(c, "update", *row)
	c.JSON(http.StatusOK, toBuildProviderJSON(*row))
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

// runtimeHostJSON is one static hosts entry of the default runtime profile.
type runtimeHostJSON struct {
	IP    string   `json:"ip"`
	Names []string `json:"names"`
}

// runtimeSettingsJSON is the API view of the build runtime settings: the
// default runtime profile's hosts / registry wiring.
type runtimeSettingsJSON struct {
	Hosts              []runtimeHostJSON   `json:"hosts"`
	InsecureRegistries []string            `json:"insecureRegistries"`
	Mirrors            map[string][]string `json:"mirrors"`
}

// runtimeOverride renders a persisted settings row as the default-profile
// fields it overrides, in buildfleet config shape.
func runtimeOverride(row model.BuildRuntimeSetting) (hosts []config.HostEntry, insecure []string, mirrors map[string][]string) {
	for _, h := range row.Hosts {
		hosts = append(hosts, config.HostEntry{IP: h.IP, Names: h.Names})
	}
	return hosts, row.InsecureRegistries, row.Mirrors
}

// runtimeSettingsView renders a default runtime profile for the API, with
// empty collections as [] / {} rather than null.
func runtimeSettingsView(r config.Runtime) runtimeSettingsJSON {
	out := runtimeSettingsJSON{
		Hosts:              make([]runtimeHostJSON, 0, len(r.Hosts)),
		InsecureRegistries: make([]string, 0, len(r.InsecureRegistries)),
		Mirrors:            make(map[string][]string, len(r.Mirrors)),
	}
	for _, h := range r.Hosts {
		out.Hosts = append(out.Hosts, runtimeHostJSON{IP: h.IP, Names: h.Names})
	}
	out.InsecureRegistries = append(out.InsecureRegistries, r.InsecureRegistries...)
	for host, sources := range r.Mirrors {
		out.Mirrors[host] = append([]string(nil), sources...)
	}
	return out
}

// validateRuntimeSettings trims and checks the PUT body: every hosts entry
// needs an ip and at least one name, insecure entries must be http(s) URLs,
// mirrors need a non-empty host and at least one source.
func validateRuntimeSettings(s *runtimeSettingsJSON) error {
	for i, h := range s.Hosts {
		names := make([]string, 0, len(h.Names))
		for _, n := range h.Names {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
		h.IP, h.Names = strings.TrimSpace(h.IP), names
		if h.IP == "" || len(h.Names) == 0 {
			return fmt.Errorf("hosts[%d]: ip and at least one name are required", i)
		}
		s.Hosts[i] = h
	}
	insecure := make([]string, 0, len(s.InsecureRegistries))
	for i, raw := range s.InsecureRegistries {
		entry := strings.TrimSpace(raw)
		if !strings.HasPrefix(entry, "http://") && !strings.HasPrefix(entry, "https://") {
			return fmt.Errorf("insecureRegistries[%d]: must start with http:// or https://", i)
		}
		insecure = append(insecure, entry)
	}
	s.InsecureRegistries = insecure
	mirrors := make(map[string][]string, len(s.Mirrors))
	for host, sources := range s.Mirrors {
		host = strings.TrimSpace(host)
		cleaned := make([]string, 0, len(sources))
		for _, m := range sources {
			if m = strings.TrimSpace(m); m != "" {
				cleaned = append(cleaned, m)
			}
		}
		if host == "" || len(cleaned) == 0 {
			return fmt.Errorf("mirrors: host and at least one mirror source are required")
		}
		mirrors[host] = cleaned
	}
	s.Mirrors = mirrors
	return nil
}

// authorizeRuntimeSettings judges the settings surface like the provider
// CRUD it sits next to: workflows verb on the request's own cluster.
func authorizeRuntimeSettings(c *gin.Context, verb string) bool {
	cs := c.MustGet("cluster").(*cluster.ClientSet)
	return authorizeProvider(c, verb, cs.Name, "")
}

// handleGetRuntimeSettings serves the persisted runtime settings, falling
// back to the config file's currently effective values when nothing was
// persisted yet.
func (d *Deps) handleGetRuntimeSettings(c *gin.Context) {
	if !authorizeRuntimeSettings(c, "get") {
		return
	}
	def := d.cfg.DefaultRuntime()
	row, err := model.GetBuildRuntimeSetting()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if row != nil {
		hosts, insecure, mirrors := runtimeOverride(*row)
		def = config.Runtime{Hosts: hosts, InsecureRegistries: insecure, Mirrors: mirrors}
	}
	c.JSON(http.StatusOK, runtimeSettingsView(def))
}

// handleUpdateRuntimeSettings validates and persists the settings, applies
// them to the in-memory default profile and remounts every managed provider,
// so the new hosts/registry wiring reaches running buildkitd provisioning.
func (d *Deps) handleUpdateRuntimeSettings(c *gin.Context) {
	if !authorizeRuntimeSettings(c, "update") {
		return
	}
	var req runtimeSettingsJSON
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request body: %v", err)})
		return
	}
	if err := validateRuntimeSettings(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	row := model.BuildRuntimeSetting{InsecureRegistries: req.InsecureRegistries, Mirrors: req.Mirrors}
	for _, h := range req.Hosts {
		row.Hosts = append(row.Hosts, model.BuildRuntimeHost{IP: h.IP, Names: h.Names})
	}
	if err := model.SaveBuildRuntimeSetting(&row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	hosts, insecure, mirrors := runtimeOverride(row)
	def := d.cfg.DefaultRuntime()
	def.Hosts, def.InsecureRegistries, def.Mirrors = hosts, insecure, mirrors
	d.cfg.SetDefaultRuntime(def)
	d.remountManagedProviders(c.Request.Context())
	c.JSON(http.StatusOK, runtimeSettingsView(def))
}

// remountManagedProviders re-registers every persisted provider so the
// mutated default profile reaches their buildkitd instances; a single
// failure is logged and the rest proceed.
func (d *Deps) remountManagedProviders(ctx context.Context) {
	rows, err := model.ListBuildProviders()
	if err != nil {
		klog.Errorf("failed to list managed build providers for remount: %v", err)
		return
	}
	for i := range rows {
		name := rows[i].Name
		if err := d.app.RemoveProvider(name); err != nil {
			// Unregistered runtime (restore previously skipped it) — the
			// register below still applies the new shape.
			klog.Warningf("build provider %s was not registered while remounting: %v", name, err)
		}
		pc, err := d.buildProviderConfig(rows[i])
		if err != nil {
			klog.Errorf("skipping build provider %s while remounting: %v", name, err)
			continue
		}
		if err := d.app.RegisterK8sProvider(ctx, pc); err != nil {
			klog.Errorf("failed to re-register build provider %s: %v", name, err)
		}
	}
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
