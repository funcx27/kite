package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/funcx27/kite/modules/workflow/internal/spec"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// buildSettingResponse is the API view of one build-cluster default row; the
// empty Namespace is the global default row.
type buildSettingResponse struct {
	Namespace string    `json:"namespace"`
	Provider  string    `json:"provider"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// upsertBuildSettingRequest is the PUT body. The namespace rides the path
// (empty path = the global default row); removing a default goes through
// DELETE, so an empty provider is refused.
type upsertBuildSettingRequest struct {
	Provider string `json:"provider" binding:"required"`
}

func toBuildSettingResponse(s *store.BuildSetting) buildSettingResponse {
	return buildSettingResponse{
		Namespace: s.Namespace,
		Provider:  s.Provider,
		UpdatedAt: s.UpdatedAt,
	}
}

// validBuildSettingNamespace validates the namespace path parameter: a
// DNS-1123 label, or empty for the global default row.
func validBuildSettingNamespace(namespace string) bool {
	return namespace == "" || spec.ValidateDNS1123Label(namespace, 63)
}

// ListBuildSettings returns the stored build-cluster defaults plus the
// host's available provider names. Rows are filtered with the same per-row
// get check the workflows list applies.
func (h *Handler) ListBuildSettings(c *gin.Context) {
	cluster := c.GetString(clusterNameContextKey)
	var rows []store.BuildSetting
	if err := h.store.DB().Order("namespace").Find(&rows).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	out := make([]buildSettingResponse, 0, len(rows))
	for i := range rows {
		if !h.deps.Authorize(c, authzResource, "get", cluster, rows[i].Namespace) {
			continue
		}
		out = append(out, toBuildSettingResponse(&rows[i]))
	}
	c.JSON(http.StatusOK, gin.H{"settings": out, "providers": h.deps.ProviderNames()})
}

// UpsertBuildSetting creates or replaces the build-cluster default of the
// path namespace; the empty path parameter targets the global default row.
func (h *Handler) UpsertBuildSetting(c *gin.Context) {
	namespace := c.Param("namespace")
	if !validBuildSettingNamespace(namespace) {
		h.fail(c, http.StatusBadRequest, nil, "namespace must be a DNS-1123 label")
		return
	}
	var req upsertBuildSettingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	cluster := c.GetString(clusterNameContextKey)
	if !h.authorize(c, "update", cluster, namespace) {
		return
	}
	// String condition, not a struct: a struct Where drops zero-value fields,
	// so the global default row (empty namespace) would match any existing row.
	// Namespace rides in Assign because FirstOrCreate cannot infer create
	// attributes from a string condition.
	var setting store.BuildSetting
	if err := h.store.DB().
		Where("namespace = ?", namespace).
		Assign(store.BuildSetting{Namespace: namespace, Provider: req.Provider}).
		FirstOrCreate(&setting).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	h.recordAudit(c, store.HistoryEntry{
		Cluster:      cluster,
		ResourceType: authzResource,
		ResourceName: "build-settings",
		Namespace:    namespace,
		Operation:    "update",
		YAML:         req.Provider,
	})
	c.JSON(http.StatusOK, toBuildSettingResponse(&setting))
}

// DeleteBuildSetting removes the path namespace's build-cluster default; the
// empty path parameter targets the global default row.
func (h *Handler) DeleteBuildSetting(c *gin.Context) {
	namespace := c.Param("namespace")
	if !validBuildSettingNamespace(namespace) {
		h.fail(c, http.StatusBadRequest, nil, "namespace must be a DNS-1123 label")
		return
	}
	cluster := c.GetString(clusterNameContextKey)
	if !h.authorize(c, "delete", cluster, namespace) {
		return
	}
	res := h.store.DB().Where("namespace = ?", namespace).Delete(&store.BuildSetting{})
	if res.Error != nil {
		h.fail(c, http.StatusInternalServerError, res.Error, "")
		return
	}
	if res.RowsAffected == 0 {
		h.fail(c, http.StatusNotFound, nil, "build setting not found")
		return
	}
	h.recordAudit(c, store.HistoryEntry{
		Cluster:      cluster,
		ResourceType: authzResource,
		ResourceName: "build-settings",
		Namespace:    namespace,
		Operation:    "delete",
	})
	c.JSON(http.StatusOK, gin.H{"message": "build setting deleted"})
}
