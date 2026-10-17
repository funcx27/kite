package credentialsadapter

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	"github.com/funcx27/kite/modules/credentials"
	"github.com/zxh326/kite/pkg/model"
)

// defaultRegistryView is the GET/PUT response shape: the stored pointer plus
// a validity flag. A dangling pointer (credential deleted or host removed
// from its payload) reports valid=false instead of an error, so clients and
// the build path treat it as "no default configured".
type defaultRegistryView struct {
	Credential string `json:"credential"`
	Host       string `json:"host"`
	Valid      bool   `json:"valid"`
}

type defaultRegistryRequest struct {
	Credential string `json:"credential" binding:"required"`
	Host       string `json:"host" binding:"required"`
}

// registerDefaultRegistryRoutes mounts the default-registry pointer API next
// to the module's CRUD surface. Like the module routes it sits before the
// host RBAC middleware and self-authorizes against the "credentials"
// pseudo-resource (GET=get, PUT=update). The "_default-registry" segment
// starts with an underscore, which credential names (DNS-1123 labels) can
// never do, so the static routes can never shadow the /:name CRUD routes —
// gin prefers static segments over the param one.
func (d *Deps) registerDefaultRegistryRoutes(g *gin.RouterGroup) {
	g.GET("/_default-registry", d.getDefaultRegistry)
	g.PUT("/_default-registry", d.putDefaultRegistry)
}

// authorizeDefaultRegistry runs the module-side permission check and writes
// the 403 response on denial, mirroring the module handler's authorize.
func (d *Deps) authorizeDefaultRegistry(c *gin.Context, verb string) bool {
	if d.Authorize(c, verb) {
		return true
	}
	c.AbortWithStatusJSON(http.StatusForbidden,
		gin.H{"error": "user does not have permission to " + verb + " " + authzResource})
	return false
}

// getDefaultRegistry echoes the stored pointer and its validity. No stored
// pointer answers with an empty, invalid view rather than 404: "no default"
// is the normal state, not a missing resource.
func (d *Deps) getDefaultRegistry(c *gin.Context) {
	if !d.authorizeDefaultRegistry(c, "get") {
		return
	}
	credential, host, ok := d.ResolveDefaultRegistry()
	c.JSON(http.StatusOK, defaultRegistryView{Credential: credential, Host: host, Valid: ok})
}

// putDefaultRegistry stores the pointer after validating it against the
// credential center: the credential must exist and its decrypted registry
// entries must carry the exact host. This keeps the pointer from ever naming
// a host the build path would not authenticate.
func (d *Deps) putDefaultRegistry(c *gin.Context) {
	var req defaultRegistryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "credential and host are required"})
		return
	}
	if !d.authorizeDefaultRegistry(c, "update") {
		return
	}
	cred, err := d.Resolve(req.Credential)
	if err != nil {
		if errors.Is(err, credentials.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("credential %q not found", req.Credential)})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "resolve credential: " + err.Error()})
		return
	}
	if !registryHasHost(cred, req.Host) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("credential %q has no registry entry for host %q", req.Credential, req.Host),
		})
		return
	}
	if err := model.SaveDefaultRegistrySetting(&model.DefaultRegistrySetting{
		Credential: req.Credential,
		Host:       req.Host,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save default registry setting: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, defaultRegistryView{Credential: req.Credential, Host: req.Host, Valid: true})
}

// ResolveDefaultRegistry validates the persisted default-registry pointer
// against the credential center and returns it; ok is true only when the
// stored credential still exists and its decrypted registry entries still
// carry the stored host (exact key match). Resolution failures other than a
// missing credential also read as invalid — the build path must never fail
// over a broken pointer, it just keeps its explicit configuration.
func (d *Deps) ResolveDefaultRegistry() (credential, host string, ok bool) {
	row, err := model.GetDefaultRegistrySetting()
	if err != nil {
		klog.Errorf("Failed to load default registry setting: %v", err)
		return "", "", false
	}
	if row == nil {
		return "", "", false
	}
	cred, err := d.module.Resolve(row.Credential)
	if err != nil {
		return row.Credential, row.Host, false
	}
	return row.Credential, row.Host, registryHasHost(cred, row.Host)
}

// registryHasHost reports whether the decrypted payload carries an entry
// with the exact host key.
func registryHasHost(cred *credentials.Credential, host string) bool {
	for _, rc := range cred.Registry {
		if rc.Host == host {
			return true
		}
	}
	return false
}
