package api

import (
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/funcx27/kite/modules/credentials/internal/store"
)

// namePattern matches a DNS-1123 label; credential names follow the same
// naming rules as the workflow module's definitions.
var namePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// maxNameLength bounds the name column width.
const maxNameLength = 63

// registryAccount is one registry entry's non-secret account view.
type registryAccount struct {
	Host     string `json:"host"`
	Username string `json:"username"`
}

// summary is the API view of a credential: a shape summary plus non-secret
// account names. The payload is write-only — its secret material (every
// password) never leaves the process.
type summary struct {
	Name             string            `json:"name"`
	Description      string            `json:"description"`
	HasGit           bool              `json:"hasGit"`
	GitUsername      string            `json:"gitUsername"`
	RegistryHosts    []string          `json:"registryHosts"`
	RegistryAccounts []registryAccount `json:"registryAccounts"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`
}

type createRequest struct {
	Name        string         `json:"name" binding:"required"`
	Description string         `json:"description"`
	Payload     *store.Payload `json:"payload"`
}

type updateRequest struct {
	Description string         `json:"description"`
	Payload     *store.Payload `json:"payload"`
}

// summarize turns a row's payload — already decrypted in memory by the
// store's scan — into its shape summary.
func summarize(row *store.Credential) (summary, error) {
	out := summary{
		Name:             row.Name,
		Description:      row.Description,
		RegistryHosts:    []string{},
		RegistryAccounts: []registryAccount{},
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
	var p store.Payload
	if err := row.Payload.Unmarshal(&p); err != nil {
		return out, err
	}
	if p.Git != nil {
		out.HasGit = true
		out.GitUsername = p.Git.Username
	}
	for _, rc := range p.Registry {
		out.RegistryHosts = append(out.RegistryHosts, rc.Host)
		out.RegistryAccounts = append(out.RegistryAccounts,
			registryAccount{Host: rc.Host, Username: rc.Username})
	}
	return out, nil
}

// List returns every credential as its shape summary.
func (h *Handler) List(c *gin.Context) {
	if !h.authorize(c, "get") {
		return
	}
	rows, err := h.store.List()
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	out := make([]summary, 0, len(rows))
	for i := range rows {
		s, err := summarize(&rows[i])
		if err != nil {
			h.fail(c, http.StatusInternalServerError, err, "")
			return
		}
		out = append(out, s)
	}
	c.JSON(http.StatusOK, gin.H{"credentials": out, "total": len(out)})
}

// Get returns one credential's shape summary.
func (h *Handler) Get(c *gin.Context) {
	if !h.authorize(c, "get") {
		return
	}
	row, err := h.store.GetByName(c.Param("name"))
	if err != nil {
		h.failGet(c, err)
		return
	}
	s, err := summarize(row)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.JSON(http.StatusOK, s)
}

// Create validates and stores a new credential.
func (h *Handler) Create(c *gin.Context) {
	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	if len(req.Name) > maxNameLength || !namePattern.MatchString(req.Name) {
		h.fail(c, http.StatusBadRequest, nil, "name must be a DNS-1123 label of at most 63 characters")
		return
	}
	if err := validatePayload(req.Payload); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	if !h.authorize(c, "create") {
		return
	}
	taken, err := h.store.HasName(req.Name)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if taken {
		h.fail(c, http.StatusConflict, nil, "credential already exists")
		return
	}
	row := store.Credential{Name: req.Name, Description: req.Description}
	if err := row.Payload.Marshal(req.Payload); err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if err := h.store.Create(&row); err != nil {
		// The HasName pre-check races with concurrent creates; a loser of
		// the unique-index write surfaces as 409, not 500.
		if taken, terr := h.store.HasName(req.Name); terr == nil && taken {
			h.fail(c, http.StatusConflict, nil, "credential already exists")
			return
		}
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	s, err := summarize(&row)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.JSON(http.StatusCreated, s)
}

// Update replaces the description and payload of an existing credential.
func (h *Handler) Update(c *gin.Context) {
	var req updateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	if err := validatePayload(req.Payload); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	if !h.authorize(c, "update") {
		return
	}
	row, err := h.store.GetByName(c.Param("name"))
	if err != nil {
		h.failGet(c, err)
		return
	}
	row.Description = req.Description
	if err := row.Payload.Marshal(req.Payload); err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if err := h.store.Save(row); err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	s, err := summarize(row)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.JSON(http.StatusOK, s)
}

// Delete removes the named credential.
func (h *Handler) Delete(c *gin.Context) {
	if !h.authorize(c, "delete") {
		return
	}
	deleted, err := h.store.DeleteByName(c.Param("name"))
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if !deleted {
		h.fail(c, http.StatusNotFound, nil, "credential not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "credential deleted"})
}

// failGet maps store lookup failures for the :name routes.
func (h *Handler) failGet(c *gin.Context, err error) {
	if errors.Is(err, store.ErrNotFound) {
		h.fail(c, http.StatusNotFound, nil, "credential not found")
		return
	}
	h.fail(c, http.StatusInternalServerError, err, "")
}

// validatePayload rejects payloads without consumable material and entries
// missing fields the build path needs.
func validatePayload(p *store.Payload) error {
	if p == nil {
		return errors.New("payload is required")
	}
	if p.Git == nil && len(p.Registry) == 0 {
		return errors.New("payload must set git or registry")
	}
	if p.Git != nil && (p.Git.Username == "" || p.Git.Password == "") {
		return errors.New("payload.git needs username and password")
	}
	seen := map[string]bool{}
	for _, rc := range p.Registry {
		if rc.Host == "" || rc.Username == "" || rc.Password == "" {
			return errors.New("every payload.registry entry needs host, username and password")
		}
		if seen[rc.Host] {
			return errors.New("payload.registry has duplicate host " + rc.Host)
		}
		seen[rc.Host] = true
	}
	return nil
}
