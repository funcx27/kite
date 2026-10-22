package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/funcx27/kite/modules/network/internal/store"
)

// annotationNamePattern matches the name segment of a k8s qualified name:
// starting and ending alphanumeric, inner alphanumerics plus . - _.
var annotationNamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)

// annotationKeyView is one catalog entry; the numeric id is the stable
// identifier (keys may contain "/" and cannot be a path segment — contract
// change #1).
type annotationKeyView struct {
	ID           uint   `json:"id"`
	Key          string `json:"key"`
	Description  string `json:"description"`
	DefaultValue string `json:"defaultValue"`
	IngressClass string `json:"ingressClass"`
}

// toAnnotationKeyView builds the API view of a catalog row.
func toAnnotationKeyView(k store.AnnotationKey) annotationKeyView {
	return annotationKeyView{
		ID: k.ID, Key: k.Key, Description: k.Description,
		DefaultValue: k.DefaultValue, IngressClass: k.IngressClass,
	}
}

// ListAnnotationKeys (K1) returns the full catalog; read needs only a login
// (§5: no sensitive material on the path).
func (h *Handler) ListAnnotationKeys(c *gin.Context) {
	keys, err := h.store.ListAnnotationKeys()
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	out := make([]annotationKeyView, 0, len(keys))
	for i := range keys {
		out = append(out, toAnnotationKeyView(keys[i]))
	}
	c.JSON(http.StatusOK, gin.H{"annotationKeys": out})
}

type createAnnotationKeyRequest struct {
	Key          string `json:"key" binding:"required"`
	Description  string `json:"description"`
	DefaultValue string `json:"defaultValue"`
	IngressClass string `json:"ingressClass" binding:"required"`
}

// CreateAnnotationKey (K2): key shape-validated and unique, description and
// defaultValue at most 255 chars, ingressClass a DNS-1123 label.
func (h *Handler) CreateAnnotationKey(c *gin.Context) {
	var req createAnnotationKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	req.Key = strings.TrimSpace(req.Key)
	req.Description = strings.TrimSpace(req.Description)
	req.DefaultValue = strings.TrimSpace(req.DefaultValue)
	if !validateAnnotationKey(req.Key) {
		h.fail(c, http.StatusBadRequest, nil, "key must be an optional DNS-subdomain prefix, a slash, and a name of at most 63 characters (316 in total)")
		return
	}
	if len(req.Description) > 255 {
		h.fail(c, http.StatusBadRequest, nil, "description must be at most 255 characters")
		return
	}
	if len(req.DefaultValue) > 255 {
		h.fail(c, http.StatusBadRequest, nil, "defaultValue must be at most 255 characters")
		return
	}
	if !validateDNS1123Label(req.IngressClass, 63) {
		h.fail(c, http.StatusBadRequest, nil, "ingressClass must be a DNS-1123 label of at most 63 characters")
		return
	}
	k := &store.AnnotationKey{
		Key: req.Key, Description: req.Description,
		DefaultValue: req.DefaultValue, IngressClass: req.IngressClass,
	}
	if err := h.store.CreateAnnotationKey(k); err != nil {
		if store.IsUniqueViolation(err) {
			h.fail(c, http.StatusConflict, nil, "annotation key already exists: "+req.Key)
			return
		}
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.JSON(http.StatusCreated, toAnnotationKeyView(*k))
}

type updateAnnotationKeyRequest struct {
	Description  string `json:"description"`
	DefaultValue string `json:"defaultValue"`
	IngressClass string `json:"ingressClass"`
}

// UpdateAnnotationKey (K3) edits description/defaultValue/ingressClass; the
// key itself is immutable.
func (h *Handler) UpdateAnnotationKey(c *gin.Context) {
	id, ok := h.parseKeyID(c)
	if !ok {
		return
	}
	k, err := h.store.GetAnnotationKey(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		h.fail(c, http.StatusNotFound, nil, "annotation key not found")
		return
	}
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	var req updateAnnotationKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	req.Description = strings.TrimSpace(req.Description)
	req.DefaultValue = strings.TrimSpace(req.DefaultValue)
	if len(req.Description) > 255 {
		h.fail(c, http.StatusBadRequest, nil, "description must be at most 255 characters")
		return
	}
	if len(req.DefaultValue) > 255 {
		h.fail(c, http.StatusBadRequest, nil, "defaultValue must be at most 255 characters")
		return
	}
	if !validateDNS1123Label(req.IngressClass, 63) {
		h.fail(c, http.StatusBadRequest, nil, "ingressClass must be a DNS-1123 label of at most 63 characters")
		return
	}
	k.Description = req.Description
	k.DefaultValue = req.DefaultValue
	k.IngressClass = req.IngressClass
	if err := h.store.UpdateAnnotationKey(k); err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.JSON(http.StatusOK, toAnnotationKeyView(*k))
}

// DeleteAnnotationKey (K4) removes the entry unless any host row still
// carries the key (S3: the 409 carries the reference count).
func (h *Handler) DeleteAnnotationKey(c *gin.Context) {
	id, ok := h.parseKeyID(c)
	if !ok {
		return
	}
	k, err := h.store.GetAnnotationKey(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		h.fail(c, http.StatusNotFound, nil, "annotation key not found")
		return
	}
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	references, err := h.countKeyReferences(k.Key)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if references > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error":      "annotation key is referenced by " + strconv.FormatInt(references, 10) + " network host(s)",
			"references": references,
		})
		return
	}
	deleted, err := h.store.DeleteAnnotationKey(id)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if !deleted {
		h.fail(c, http.StatusNotFound, nil, "annotation key not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "annotation key deleted"})
}

type importAnnotationKeysRequest struct {
	IngressClass string `json:"ingressClass" binding:"required"`
	Text         string `json:"text" binding:"required"`
}

// ImportAnnotationKeys (K5) parses "key,description" lines (comma or tab
// separated; a bare token is a key with no description), applies the batch's
// ingressClass to every entry, and skips duplicates (against the catalog and
// within the batch) and invalid lines with a count. Empty lines are noise and
// do not count.
func (h *Handler) ImportAnnotationKeys(c *gin.Context) {
	var req importAnnotationKeysRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	req.IngressClass = strings.TrimSpace(req.IngressClass)
	if !validateDNS1123Label(req.IngressClass, 63) {
		h.fail(c, http.StatusBadRequest, nil, "ingressClass must be a DNS-1123 label of at most 63 characters")
		return
	}
	var (
		entries []store.AnnotationKey
		seen    = map[string]bool{}
		skipped int
	)
	for _, line := range strings.Split(req.Text, "\n") {
		key, description := splitImportLine(line)
		if key == "" {
			continue
		}
		if seen[key] || !validateAnnotationKey(key) || len(description) > 255 {
			skipped++
			continue
		}
		seen[key] = true
		entries = append(entries, store.AnnotationKey{
			Key: key, Description: description, IngressClass: req.IngressClass,
		})
	}
	var imported, dbSkipped int
	txErr := h.store.DB().Transaction(func(tx *gorm.DB) error {
		var err error
		imported, dbSkipped, err = store.ImportTx(tx, entries)
		return err
	})
	if txErr != nil {
		if isConcurrentWriteError(txErr) {
			h.fail(c, http.StatusConflict, nil, "annotation catalog changed concurrently, retry the import")
			return
		}
		h.fail(c, http.StatusInternalServerError, txErr, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"imported": imported, "skipped": skipped + dbSkipped})
}

// splitImportLine splits one "key,description" line on the first comma or
// tab; both sides are trimmed.
func splitImportLine(line string) (key, description string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", ""
	}
	if i := strings.IndexAny(line, ",\t"); i >= 0 {
		return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
	}
	return line, ""
}

// parseKeyID parses the :id path segment.
func (h *Handler) parseKeyID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		h.fail(c, http.StatusBadRequest, nil, "annotation key id must be a number")
		return 0, false
	}
	return uint(id), true
}

// countKeyReferences counts host rows still carrying the key, either as a
// host-level annotation (annotations JSON object) or as a service-level
// annotation inside a stored route (routes JSON array). The LIKE wildcards are
// escaped so keys containing "_" match literally.
func (h *Handler) countKeyReferences(key string) (int64, error) {
	var count int64
	pattern := `%"` + escapeLike(key) + `"%`
	if err := h.store.DB().Model(&store.Host{}).
		Where("annotations LIKE ? ESCAPE '\\' OR routes LIKE ? ESCAPE '\\'", pattern, pattern).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count annotation key references: %w", err)
	}
	return count, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// validateAnnotationKey enforces the k8s qualified-name shape: an optional
// DNS-subdomain prefix, a slash, then a name segment — at most 316 characters
// in total (253 + 1 + 62).
func validateAnnotationKey(key string) bool {
	if key == "" || len(key) > 316 {
		return false
	}
	name := key
	if i := strings.LastIndex(key, "/"); i >= 0 {
		name = key[i+1:]
		if !validateDNSHost(key[:i]) {
			return false
		}
	}
	return len(name) <= 63 && annotationNamePattern.MatchString(name)
}
