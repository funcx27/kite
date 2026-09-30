package api

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/funcx27/kite/modules/application/internal/render"
	"github.com/funcx27/kite/modules/application/internal/store"
)

type previewRequest struct {
	Name      string          `json:"name" binding:"required"`
	Namespace string          `json:"namespace"`
	Spec      json.RawMessage `json:"spec" binding:"required"`
}

// PreviewRender renders a wizard spec into manifests without touching the
// database or a cluster (design §7: pure computation for the wizard preview).
func (h *Handler) PreviewRender(c *gin.Context) {
	var req previewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	if !render.ValidateDNS1123Label(req.Name, 63) {
		h.fail(c, http.StatusBadRequest, nil, "name must be a DNS-1123 label of at most 63 characters")
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}
	if !render.ValidateDNS1123Label(ns, 63) {
		h.fail(c, http.StatusBadRequest, nil, "namespace must be a DNS-1123 label")
		return
	}
	if !h.authorize(c, "get", c.GetString(clusterNameContextKey), ns) {
		return
	}
	var spec render.AppSpec
	if err := json.Unmarshal(req.Spec, &spec); err != nil {
		h.fail(c, http.StatusBadRequest, err, "spec must be a valid AppSpec JSON object")
		return
	}
	docs, err := render.Render(req.Name, spec, ns)
	if err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	yamlStr, err := store.ManifestsToYAML(docs)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"manifests": docs, "yaml": yamlStr})
}
