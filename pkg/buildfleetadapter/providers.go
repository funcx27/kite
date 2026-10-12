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
	"unicode"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"k8s.io/klog/v2"

	"github.com/funcx27/kite/modules/buildfleet/config"
	bfresource "github.com/funcx27/kite/modules/buildfleet/resource"
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

// providerConfig validates the row and returns the fleet provider config.
// k8s rows validate the target cluster (exists, connected, kubeconfig-
// exportable) and point at the exported kubeconfig; docker rows carry the
// daemon endpoint and optional TLS file paths, with no cluster involved.
func (d *Deps) providerConfig(row model.BuildProvider) (config.Provider, error) {
	if effectiveType(row) == "docker" {
		return d.dockerProviderConfig(row), nil
	}
	return d.k8sProviderConfig(row)
}

// dockerProviderConfig renders the row as a fleet docker provider config.
// Registration-time endpoint validation happens in docker.New (remote tcp
// daemons cannot bind-mount the shared buildkitd.toml).
func (d *Deps) dockerProviderConfig(row model.BuildProvider) config.Provider {
	pc := config.Provider{
		Type:    "docker",
		Name:    row.Name,
		Address: row.Address,
		Runtime: row.Runtime,
	}
	if row.CPU > 0 {
		pc.Total.CPU = row.CPU
	}
	if row.Memory != "" {
		pc.Total.Memory = row.Memory
	}
	if row.TLS != nil {
		pc.TLS = &config.TLS{
			CA:                 row.TLS.CA,
			Cert:               row.TLS.Cert,
			Key:                row.TLS.Key,
			InsecureSkipVerify: row.TLS.InsecureSkipVerify,
		}
	}
	return pc
}

// k8sProviderConfig validates the row's target cluster and returns the fleet
// provider config pointing at the exported kubeconfig. In-cluster installs
// keep Kubeconfig empty: the provider then uses the same ServiceAccount
// identity as the kite host. Out-of-cluster targets get PortForward on:
// runtime pods are addressed through an apiserver tunnel, since the daemon
// outside the cluster cannot resolve in-cluster Service DNS.
func (d *Deps) k8sProviderConfig(row model.BuildProvider) (config.Provider, error) {
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

// configProviderName reports whether name belongs to the loaded buildfleet
// config file — those providers are read-only through this API (create and
// the enabled toggle reject them).
func (d *Deps) configProviderName(name string) bool {
	return slices.Contains(d.providerNames, name)
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
	api.PUT("/providers/:name/enabled", d.handleSetProviderEnabled)
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

type buildProviderTLSJSON struct {
	CA                 string `json:"ca"`
	Cert               string `json:"cert"`
	Key                string `json:"key"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
}

type buildProviderJSON struct {
	Type                string                  `json:"type"`
	Name                string                  `json:"name"`
	Enabled             bool                    `json:"enabled"`
	Cluster             string                  `json:"cluster"`
	Namespace           string                  `json:"namespace"`
	Address             string                  `json:"address"`
	TLS                 *buildProviderTLSJSON   `json:"tls"`
	CPU                 float64                 `json:"cpu"`
	Memory              string                  `json:"memory"`
	Runtime             string                  `json:"runtime"`
	ReadyTimeoutSeconds int                     `json:"readyTimeoutSeconds"`
	PodCPU              float64                 `json:"podCpu"`
	PodMemoryGi         float64                 `json:"podMemoryGi"`
	Cache               *buildProviderCacheJSON `json:"cache"`
}

// effectiveType maps pre-type rows ("") onto k8s.
func effectiveType(row model.BuildProvider) string {
	if row.Type == "docker" {
		return "docker"
	}
	return "k8s"
}

func toBuildProviderJSON(r model.BuildProvider) buildProviderJSON {
	out := buildProviderJSON{
		Type:                effectiveType(r),
		Name:                r.Name,
		Enabled:             r.Enabled,
		Cluster:             r.Cluster,
		Namespace:           r.Namespace,
		Address:             r.Address,
		CPU:                 r.CPU,
		Memory:              r.Memory,
		Runtime:             r.Runtime,
		ReadyTimeoutSeconds: r.ReadyTimeoutSeconds,
		PodCPU:              r.PodCPU,
		PodMemoryGi:         r.PodMemoryGi,
	}
	if r.TLS != nil {
		out.TLS = &buildProviderTLSJSON{
			CA:                 r.TLS.CA,
			Cert:               r.TLS.Cert,
			Key:                r.TLS.Key,
			InsecureSkipVerify: r.TLS.InsecureSkipVerify,
		}
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

// registerProvider attaches one provider config to the fleet runtime,
// dispatching on its type.
func (d *Deps) registerProvider(ctx context.Context, pc config.Provider) error {
	if pc.Type == "docker" {
		return d.app.RegisterDockerProvider(ctx, pc)
	}
	return d.app.RegisterK8sProvider(ctx, pc)
}

// providerDefaultsJSON surfaces the effective built-in defaults so the UI
// displays them instead of empty fields: the module's ready-timeout default,
// the default runtime profile's pod shape, and the config's per-type
// providerDefaults (k8s cache fields, docker capacity).
type providerDefaultsJSON struct {
	ReadyTimeoutSeconds int     `json:"readyTimeoutSeconds"`
	PodCPU              float64 `json:"podCpu"`
	PodMemoryGi         float64 `json:"podMemoryGi"`
	CacheImage          string  `json:"cacheImage"`
	CacheSize           string  `json:"cacheSize"`
	CacheGCSchedule     string  `json:"cacheGcSchedule"`
	DockerCPU           float64 `json:"dockerCpu"`
	DockerMemory        string  `json:"dockerMemory"`
}

func (d *Deps) providerDefaults() providerDefaultsJSON {
	out := providerDefaultsJSON{
		// The module's pod-Ready poll default (ReadyTimeoutParsed, empty = 60s).
		ReadyTimeoutSeconds: 60,
	}
	def := d.cfg.DefaultRuntime()
	out.PodCPU = def.DefaultResources.CPU
	if out.PodMemoryGi == 0 && def.DefaultResources.Memory != "" {
		if b, err := bfresource.ParseQuantity(def.DefaultResources.Memory); err == nil {
			out.PodMemoryGi = float64(b) / float64(bfresource.Giga)
		}
	}
	if k8sDef, ok := d.cfg.ProviderDefaults["k8s"]; ok && k8sDef.Cache != nil {
		out.CacheImage = k8sDef.Cache.Image
		out.CacheSize = k8sDef.Cache.Size
		out.CacheGCSchedule = k8sDef.Cache.GCSchedule
	}
	if dockerDef, ok := d.cfg.ProviderDefaults["docker"]; ok {
		out.DockerCPU = dockerDef.Total.CPU
		out.DockerMemory = dockerDef.Total.Memory
	}
	return out
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
	c.JSON(http.StatusOK, gin.H{"providers": out, "configProviders": d.providerNames, "runtimes": d.runtimeNames(), "defaults": d.providerDefaults()})
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
	Name string `json:"name" binding:"required"`
	// Type selects the runtime kind; empty = k8s.
	Type    string `json:"type"`
	Cluster string `json:"cluster"`
	// Namespace is required for k8s providers, rejected for docker ones.
	Namespace string                `json:"namespace"`
	Address   string                `json:"address"`
	TLS       *buildProviderTLSJSON `json:"tls"`
	CPU       float64               `json:"cpu"`
	Memory    string                `json:"memory"`
	buildProviderFields
}

// applyDockerFields copies the docker-only request fields onto the row; an
// all-empty TLS block stores as nil.
func applyDockerFields(row *model.BuildProvider, address string, tls *buildProviderTLSJSON) {
	row.Address = strings.TrimSpace(address)
	if tls != nil {
		parsed := &model.BuildProviderTLS{
			CA:                 strings.TrimSpace(tls.CA),
			Cert:               strings.TrimSpace(tls.Cert),
			Key:                strings.TrimSpace(tls.Key),
			InsecureSkipVerify: tls.InsecureSkipVerify,
		}
		if parsed.CA != "" || parsed.Cert != "" || parsed.Key != "" || parsed.InsecureSkipVerify {
			row.TLS = parsed
		}
	}
}

// handleCreateProvider registers a build provider from the settings UI:
// validate → (k8s: export kubeconfig) → register with the fleet runtime →
// persist.
func (d *Deps) handleCreateProvider(c *gin.Context) {
	var req createBuildProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request body: %v", err)})
		return
	}
	ptype := req.Type
	if ptype == "" {
		ptype = "k8s"
	}
	if ptype != "k8s" && ptype != "docker" {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("type must be k8s or docker, got %q", req.Type)})
		return
	}
	row := model.BuildProvider{
		Type:      ptype,
		Name:      strings.TrimSpace(req.Name),
		Enabled:   true,
		Cluster:   strings.TrimSpace(req.Cluster),
		Namespace: strings.TrimSpace(req.Namespace),
		CPU:       req.CPU,
		Memory:    strings.TrimSpace(req.Memory),
	}
	if !validDNS1123Label(row.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must be a DNS-1123 label (lowercase letters, digits and dashes, 63 chars max)"})
		return
	}
	if ptype == "docker" {
		// Docker providers build on the kite host's daemon: no cluster row,
		// no namespace, no kubeconfig export.
		row.Cluster, row.Namespace = "", ""
		applyDockerFields(&row, req.Address, req.TLS)
	} else {
		if row.Cluster == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cluster is required"})
			return
		}
		if !validDNS1123Label(row.Namespace) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "namespace must be a DNS-1123 label (lowercase letters, digits and dashes, 63 chars max)"})
			return
		}
	}
	if err := d.applyBuildProviderFields(&row, req.buildProviderFields); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// The gate targets the row's cluster: this is the cluster whose
	// kubeconfig gets exported and where buildkitd is provisioned. Docker
	// rows carry empty cluster/namespace, i.e. the host-wide judgment.
	if !authorizeProvider(c, "create", row.Cluster, row.Namespace) {
		return
	}
	if d.configProviderName(row.Name) {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("provider %q is already defined in the buildfleet config", row.Name)})
		return
	}
	if _, err := model.GetBuildProviderByName(row.Name); err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("build provider %q already exists", row.Name)})
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	pc, err := d.providerConfig(row)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := d.registerProvider(c.Request.Context(), pc); err != nil {
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
// name and the kubeconfig cache file is keyed by it. The type is immutable
// too (recreating is the honest path between docker and k8s).
type updateBuildProviderRequest struct {
	Cluster   string                `json:"cluster"`
	Namespace string                `json:"namespace"`
	Address   string                `json:"address"`
	TLS       *buildProviderTLSJSON `json:"tls"`
	CPU       float64               `json:"cpu"`
	Memory    string                `json:"memory"`
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
	if effectiveType(*row) == "docker" {
		row.Cluster, row.Namespace = "", ""
		applyDockerFields(row, req.Address, req.TLS)
	} else if !validDNS1123Label(row.Namespace) {
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
	newPC, err := d.providerConfig(*row)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	oldPC, pcErr := d.providerConfig(oldRow)
	// Swap the runtime registration first: a persist failure below can put
	// the old shape back, while the reverse order would leave the runtime
	// serving a shape the row no longer describes.
	if err := d.app.RemoveProvider(name); err != nil {
		// Unregistered runtime (restore previously skipped it) — the register
		// below still records the new shape.
		klog.Warningf("build provider %s was not registered before update: %v", name, err)
	}
	// Disabled rows stay unregistered; the update only swaps the stored shape.
	if row.Enabled {
		if err := d.registerProvider(c.Request.Context(), newPC); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if err := model.UpdateBuildProvider(row); err != nil {
		if rmErr := d.app.RemoveProvider(name); rmErr != nil {
			klog.Errorf("failed to unregister build provider %s after failed persist: %v", name, rmErr)
		}
		if row.Enabled && pcErr == nil {
			if reErr := d.registerProvider(c.Request.Context(), oldPC); reErr != nil {
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

// setProviderEnabledRequest is the PUT /providers/:name/enabled body.
type setProviderEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

// handleSetProviderEnabled toggles one managed provider. Disabling
// unregisters it from the fleet like deletion but keeps the row and its
// kubeconfig cache — a recoverable delete; enabling re-registers it through
// the same row→config→register path the restart restore uses.
func (d *Deps) handleSetProviderEnabled(c *gin.Context) {
	name := c.Param("name")
	if !validDNS1123Label(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must be a DNS-1123 label"})
		return
	}
	var req setProviderEnabledRequest
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
	if d.configProviderName(name) {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("provider %q is already defined in the buildfleet config", name)})
		return
	}
	if !authorizeProvider(c, "update", row.Cluster, row.Namespace) {
		return
	}
	if row.Enabled == req.Enabled {
		c.JSON(http.StatusOK, toBuildProviderJSON(*row))
		return
	}
	if req.Enabled {
		d.enableProvider(c, row)
		return
	}
	d.disableProvider(c, row)
}

// enableProvider registers a disabled row (row→config→register) and persists
// the flag; a persist failure rolls the registration back, like creation.
func (d *Deps) enableProvider(c *gin.Context, row *model.BuildProvider) {
	pc, err := d.providerConfig(*row)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := d.registerProvider(c.Request.Context(), pc); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	row.Enabled = true
	if err := model.UpdateBuildProvider(row); err != nil {
		if rmErr := d.app.RemoveProvider(row.Name); rmErr != nil {
			klog.Errorf("failed to unregister build provider %s after failed persist: %v", row.Name, rmErr)
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	recordProviderAudit(c, "update", *row)
	c.JSON(http.StatusOK, toBuildProviderJSON(*row))
}

// disableProvider persists the flag and unregisters the row from the fleet —
// the handleDeleteProvider unregister path minus the row/kubeconfig deletion.
func (d *Deps) disableProvider(c *gin.Context, row *model.BuildProvider) {
	row.Enabled = false
	if err := model.UpdateBuildProvider(row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := d.app.RemoveProvider(row.Name); err != nil {
		// The row is disabled, so the next restart sheds the registration
		// too; the runtime stays registered until then.
		klog.Errorf("failed to unregister build provider %s: %v", row.Name, err)
	}
	recordProviderAudit(c, "update", *row)
	c.JSON(http.StatusOK, toBuildProviderJSON(*row))
}

// runtimeHostJSON is one static hosts entry of the default runtime profile.
type runtimeHostJSON struct {
	IP    string   `json:"ip"`
	Names []string `json:"names"`
}

// runtimeSettingsJSON is the API view of the build runtime settings: the
// default runtime profile's hosts / registry wiring plus the two build
// images (empty image = the built-in per-mode default).
type runtimeSettingsJSON struct {
	Hosts              []runtimeHostJSON   `json:"hosts"`
	InsecureRegistries []string            `json:"insecureRegistries"`
	Mirrors            map[string][]string `json:"mirrors"`
	RootfulImage       string              `json:"rootfulImage"`
	RootlessImage      string              `json:"rootlessImage"`
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

// runtimeImages resolves the effective build images for the API view: a
// non-empty persisted value wins, otherwise the live profile's image
// (rootful from the default profile, rootless from the "rootless" profile
// when the config defines one, else empty).
func (d *Deps) runtimeImages(row *model.BuildRuntimeSetting) (rootful, rootless string) {
	rootful = d.cfg.DefaultRuntime().Image()
	if r, ok := d.cfg.RuntimeProfiles()["rootless"]; ok {
		rootless = r.Image()
	}
	if row != nil {
		if row.RootfulImage != "" {
			rootful = row.RootfulImage
		}
		if row.RootlessImage != "" {
			rootless = row.RootlessImage
		}
	}
	return rootful, rootless
}

// cleanImageRef trims one build-image ref; empty means the built-in default.
// Embedded whitespace can only be a paste error — a ref is one token.
func cleanImageRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if strings.IndexFunc(ref, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("image ref %q must not contain whitespace", ref)
	}
	return ref, nil
}

// validateRuntimeSettings trims and checks the PUT body: every hosts entry
// needs an ip and at least one name, insecure entries must be http(s) URLs,
// mirrors need a non-empty host and at least one source, image refs must be
// whitespace-free.
func validateRuntimeSettings(s *runtimeSettingsJSON) error {
	var err error
	if s.RootfulImage, err = cleanImageRef(s.RootfulImage); err != nil {
		return fmt.Errorf("rootfulImage: %w", err)
	}
	if s.RootlessImage, err = cleanImageRef(s.RootlessImage); err != nil {
		return fmt.Errorf("rootlessImage: %w", err)
	}
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
	resp := runtimeSettingsView(def)
	resp.RootfulImage, resp.RootlessImage = d.runtimeImages(row)
	c.JSON(http.StatusOK, resp)
}

// handleUpdateRuntimeSettings validates and persists the settings, applies
// them to the in-memory runtime profiles (default wiring plus the two build
// images) and remounts every managed provider, so the new hosts/registry
// wiring and images reach running buildkitd provisioning.
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
	row := model.BuildRuntimeSetting{
		InsecureRegistries: req.InsecureRegistries,
		Mirrors:            req.Mirrors,
		RootfulImage:       req.RootfulImage,
		RootlessImage:      req.RootlessImage,
	}
	for _, h := range req.Hosts {
		row.Hosts = append(row.Hosts, model.BuildRuntimeHost{IP: h.IP, Names: h.Names})
	}
	if err := model.SaveBuildRuntimeSetting(&row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	hosts, insecure, mirrors := runtimeOverride(row)
	d.cfg.SetRuntimeImages(row.RootfulImage, row.RootlessImage)
	def := d.cfg.DefaultRuntime()
	def.Hosts, def.InsecureRegistries, def.Mirrors = hosts, insecure, mirrors
	d.cfg.SetDefaultRuntime(def)
	d.remountManagedProviders(c.Request.Context())
	resp := runtimeSettingsView(def)
	resp.RootfulImage, resp.RootlessImage = d.runtimeImages(&row)
	c.JSON(http.StatusOK, resp)
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
		// Disabled rows stay unregistered; the enabled endpoint mounts them.
		if !rows[i].Enabled {
			continue
		}
		name := rows[i].Name
		if err := d.app.RemoveProvider(name); err != nil {
			// Unregistered runtime (restore previously skipped it) — the
			// register below still applies the new shape.
			klog.Warningf("build provider %s was not registered while remounting: %v", name, err)
		}
		pc, err := d.providerConfig(rows[i])
		if err != nil {
			klog.Errorf("skipping build provider %s while remounting: %v", name, err)
			continue
		}
		if err := d.registerProvider(ctx, pc); err != nil {
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
		// Disabled rows stay unregistered; the enabled endpoint mounts them.
		if !rows[i].Enabled {
			continue
		}
		pc, err := d.providerConfig(rows[i])
		if err != nil {
			klog.Errorf("skipping build provider %s: %v", rows[i].Name, err)
			continue
		}
		if err := d.registerProvider(ctx, pc); err != nil {
			klog.Errorf("failed to register build provider %s: %v", rows[i].Name, err)
		}
	}
}
