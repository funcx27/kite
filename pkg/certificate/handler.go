package certificate

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/zxh326/kite/pkg/model"
	"gorm.io/gorm"
)

// dns1123Pattern matches an RFC 1123 label: lowercase alphanumerics and
// dashes, starting and ending with an alphanumeric.
var dns1123Pattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// certificateMeta is the metadata view shared by the admin and module
// endpoints: no PEM chain and never the key.
type certificateMeta struct {
	ID          uint      `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CommonName  string    `json:"commonName"`
	DNSNames    []string  `json:"dnsNames"`
	Issuer      string    `json:"issuer"`
	NotBefore   time.Time `json:"notBefore"`
	NotAfter    time.Time `json:"notAfter"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"createdAt"`
}

// certificateDetail adds the chain for the admin edit round trip; the key is
// write-only by design.
type certificateDetail struct {
	certificateMeta
	CertPEM   string    `json:"certPem"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// metaColumns lists the columns every read may load; cert_pem joins them for
// the admin detail view, key_pem only ever loads on the write paths.
var metaColumns = []string{
	"id", "name", "description", "common_name", "dns_names", "issuer",
	"not_before", "not_after", "fingerprint", "created_at", "updated_at",
}

// RegisterAdminRoutes mounts the management endpoints inside the host's admin
// permission group: list, detail, create, update, delete.
func RegisterAdminRoutes(group *gin.RouterGroup) {
	group.GET("", HandleListCertificates)
	group.POST("", HandleCreateCertificate)
	group.GET("/:id", HandleGetCertificate)
	group.PUT("/:id", HandleUpdateCertificate)
	group.DELETE("/:id", HandleDeleteCertificate)
}

// RegisterModuleRoutes mounts the read-only metadata listing the application
// wizard's certificate picker consumes.
func RegisterModuleRoutes(group *gin.RouterGroup) {
	group.GET("", HandleListCertificates)
}

// HandleListCertificates returns every certificate's metadata.
func HandleListCertificates(c *gin.Context) {
	var rows []model.Certificate
	if err := model.DB.Select(metaColumns).Order("name").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]certificateMeta, 0, len(rows))
	for i := range rows {
		out = append(out, metaOf(&rows[i]))
	}
	c.JSON(http.StatusOK, gin.H{"certificates": out})
}

// HandleGetCertificate returns one certificate's metadata plus its chain for
// the edit form; the key is never returned.
func HandleGetCertificate(c *gin.Context) {
	detailColumns := append(append([]string(nil), metaColumns...), "cert_pem")
	row, ok := findCertificateOr404(c, detailColumns)
	if !ok {
		return
	}
	detail := certificateDetail{CertPEM: row.CertPEM, UpdatedAt: row.UpdatedAt}
	detail.certificateMeta = metaOf(row)
	c.JSON(http.StatusOK, detail)
}

// HandleCreateCertificate validates and stores a new certificate; name is
// immutable afterwards because references are not tracked in v1.
func HandleCreateCertificate(c *gin.Context) {
	var req struct {
		Name        string `json:"name" binding:"required"`
		Description string `json:"description"`
		CertPEM     string `json:"certPem" binding:"required"`
		KeyPEM      string `json:"keyPem" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !validateName(req.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "certificate name must be a DNS-1123 label: lowercase letters, digits and '-', starting and ending alphanumeric, at most 63 characters"})
		return
	}
	bundle, err := ParseBundle(req.CertPEM, req.KeyPEM)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var count int64
	if err := model.DB.Model(&model.Certificate{}).Where("name = ?", req.Name).
		Count(&count).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "certificate name " + req.Name + " already exists"})
		return
	}
	row := &model.Certificate{
		Name:        req.Name,
		Description: req.Description,
		CertPEM:     bundle.CertPEM,
		KeyPEM:      model.SecretString(bundle.KeyPEM),
		CommonName:  bundle.CommonName,
		DNSNames:    bundle.DNSNames,
		Issuer:      bundle.Issuer,
		NotBefore:   bundle.NotBefore,
		NotAfter:    bundle.NotAfter,
		Fingerprint: bundle.Fingerprint,
		ContentHash: bundle.ContentHash,
	}
	if err := model.DB.Create(row).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	detail := certificateDetail{CertPEM: row.CertPEM, UpdatedAt: row.UpdatedAt}
	detail.certificateMeta = metaOf(row)
	c.JSON(http.StatusCreated, detail)
}

// HandleUpdateCertificate applies partial edits: description alone, a new
// chain (which then requires the key again), or only a new key (validated
// against the stored chain). The name is not editable.
func HandleUpdateCertificate(c *gin.Context) {
	row, ok := findCertificateOr404(c, nil)
	if !ok {
		return
	}
	var req struct {
		Description *string `json:"description"`
		CertPEM     string  `json:"certPem"`
		KeyPEM      string  `json:"keyPem"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Description != nil {
		row.Description = *req.Description
	}
	switch {
	case req.CertPEM != "":
		bundle, err := ParseCertificate(req.CertPEM)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if bundle.CertPEM == row.CertPEM {
			// Unchanged chain: only a submitted key can move the content hash.
			if req.KeyPEM == "" {
				break
			}
			normKey, hash, err := MatchKey(row.CertPEM, req.KeyPEM)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			row.KeyPEM = model.SecretString(normKey)
			row.ContentHash = hash
			break
		}
		if req.KeyPEM == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "certificate changed: keyPem is required to validate the new certificate"})
			return
		}
		full, err := ParseBundle(req.CertPEM, req.KeyPEM)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		row.CertPEM = full.CertPEM
		row.KeyPEM = model.SecretString(full.KeyPEM)
		row.CommonName = full.CommonName
		row.DNSNames = full.DNSNames
		row.Issuer = full.Issuer
		row.NotBefore = full.NotBefore
		row.NotAfter = full.NotAfter
		row.Fingerprint = full.Fingerprint
		row.ContentHash = full.ContentHash
	case req.KeyPEM != "":
		normKey, hash, err := MatchKey(row.CertPEM, req.KeyPEM)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		row.KeyPEM = model.SecretString(normKey)
		row.ContentHash = hash
	}
	if err := model.DB.Save(row).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	detail := certificateDetail{CertPEM: row.CertPEM, UpdatedAt: row.UpdatedAt}
	detail.certificateMeta = metaOf(row)
	c.JSON(http.StatusOK, detail)
}

// HandleDeleteCertificate removes a certificate; v1 performs no reference
// check, apps that still name it fail their next publish with a clear error.
func HandleDeleteCertificate(c *gin.Context) {
	row, ok := findCertificateOr404(c, nil)
	if !ok {
		return
	}
	if err := model.DB.Delete(row).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "certificate " + row.Name + " deleted"})
}

// findCertificateOr404 loads one row by path id; columns nil means all. The
// 404/500 response is written when loading fails.
func findCertificateOr404(c *gin.Context, columns []string) (*model.Certificate, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid certificate id"})
		return nil, false
	}
	var row model.Certificate
	query := model.DB
	if columns != nil {
		query = query.Select(columns)
	}
	if err := query.First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "certificate not found"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return nil, false
	}
	return &row, true
}

func validateName(name string) bool {
	return len(name) >= 1 && len(name) <= 63 && dns1123Pattern.MatchString(name)
}

func metaOf(row *model.Certificate) certificateMeta {
	dns := row.DNSNames
	if dns == nil {
		dns = []string{}
	}
	return certificateMeta{
		ID:          row.ID,
		Name:        row.Name,
		Description: row.Description,
		CommonName:  row.CommonName,
		DNSNames:    dns,
		Issuer:      row.Issuer,
		NotBefore:   row.NotBefore,
		NotAfter:    row.NotAfter,
		Fingerprint: row.Fingerprint,
		CreatedAt:   row.CreatedAt,
	}
}
