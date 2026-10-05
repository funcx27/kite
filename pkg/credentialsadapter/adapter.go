// Package credentialsadapter wires kite capabilities to the credentials
// module's Deps interface and exposes the Resolver that the buildfleet and
// workflow adapters consume for credential ref resolution. It is a kite-side
// bridge package (modules/README.md contract §1).
package credentialsadapter

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/funcx27/kite/modules/buildfleet/creds"
	"github.com/funcx27/kite/modules/buildfleet/fleet"
	"github.com/funcx27/kite/modules/credentials"
	"github.com/zxh326/kite/pkg/model"
	"github.com/zxh326/kite/pkg/rbac"
	"github.com/zxh326/kite/pkg/utils"
)

// authzResource is the pseudo-resource the module API authorizes against.
const authzResource = "credentials"

// encryptionErrorPrefix marks the sentinel string utils.EncryptString
// returns on failure instead of an error (modules/README.md contract §4).
const encryptionErrorPrefix = "encryption_error: "

// Deps implements credentials.Deps on top of kite's RBAC roles and at-rest
// encryption, and owns the module instance for the host.
type Deps struct {
	module *credentials.Module
}

var _ credentials.Deps = (*Deps)(nil)

// FleetCredentialInjector is the build-path surface the credential center
// exposes: resolve the git/registry credential refs and inject them into a
// fleet build request. Both the buildfleet REST facade and the workflow
// in-process build consume it, so the resolve-and-inject mapping has exactly
// one implementation.
type FleetCredentialInjector interface {
	// InjectFleetCredentials resolves gitRef/registryRef and injects git auth
	// plus the registry table into freq. Errors never contain secret material.
	InjectFleetCredentials(freq *fleet.Request, gitRef, registryRef string) error
}

// New builds the adapter, wires the module's secret codec and constructs
// the module; it runs at assembly, before any module table is read.
func New() *Deps {
	d := &Deps{}
	credentials.SetSecretCodec(d.EncryptSecret, d.DecryptSecret)
	d.module = credentials.New(d)
	return d
}

// Migrate creates or updates the credentials module's tables on db.
func Migrate(db *gorm.DB) error {
	return credentials.Migrate(db)
}

// RegisterRoutes mounts the module API under /credentials. The host must
// call it before applying its RBAC middleware: the URL shapes are
// non-resource and handlers authorize themselves against "credentials".
func (d *Deps) RegisterRoutes(api *gin.RouterGroup) {
	d.module.RegisterRoutes(api)
}

// Resolve decrypts the named credential (Resolver implementation).
func (d *Deps) Resolve(name string) (*credentials.Credential, error) {
	return d.module.Resolve(name)
}

// InjectFleetCredentials resolves the two refs and injects them into the
// request: gitRef's basic auth onto the git source, registryRef's registry
// entries into the per-request creds table (longest-prefix match happens
// fleet-side). Each ref is resolved only when non-empty; two empty refs do
// nothing. A git credential on a non-git source is a client error, not a
// silent anonymous build. Nil-receiver safe: an unwired center yields an
// explicit error for any non-empty ref.
func (d *Deps) InjectFleetCredentials(freq *fleet.Request, gitRef, registryRef string) error {
	if gitRef == "" && registryRef == "" {
		return nil
	}
	if d == nil {
		ref := gitRef
		if ref == "" {
			ref = registryRef
		}
		return fmt.Errorf("credential %q cannot be resolved: credential center is not wired", ref)
	}
	if gitRef != "" {
		cred, err := d.module.Resolve(gitRef)
		if err != nil {
			return fmt.Errorf("resolve credential %q: %w", gitRef, err)
		}
		if cred.Git != nil {
			if freq.Git == nil {
				return fmt.Errorf("credential %q provides git auth but the build source is not git", gitRef)
			}
			freq.Git.Username = cred.Git.Username
			freq.Git.Password = cred.Git.Password
		}
	}
	if registryRef != "" {
		cred, err := d.module.Resolve(registryRef)
		if err != nil {
			return fmt.Errorf("resolve credential %q: %w", registryRef, err)
		}
		for _, rc := range cred.Registry {
			freq.Creds = append(freq.Creds, creds.Credential{
				Kind:   creds.Registry,
				Match:  rc.Host,
				Direct: &creds.Secret{Username: rc.Username, Secret: rc.Password},
			})
		}
	}
	return nil
}

// urlCredentialPattern matches scheme://userinfo@ — module-side source
// errors embed the raw client URL, which may carry basic-auth credentials.
var urlCredentialPattern = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/@\s]+@`)

// RedactCredentials masks the userinfo of any URL in s with ***, for error
// text that leaves the process (build audit rows, workflow node errors).
func RedactCredentials(s string) string {
	return urlCredentialPattern.ReplaceAllString(s, "${1}***@")
}

func (d *Deps) Authorize(c *gin.Context, verb string) bool {
	user := c.MustGet("user").(model.User)
	return rbac.CanAccess(user, authzResource, verb, "", "")
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
