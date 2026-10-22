package api

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/funcx27/kite/modules/network/internal/store"
)

type replaceRoutesRequest struct {
	Routes []store.Route `json:"routes"`
}

type replaceAnnotationsRequest struct {
	Annotations map[string]string `json:"annotations"`
}

// ReplaceRoutes (H4) swaps the host's whole route table: paths unique and
// starting with "/", app names DNS-1123 labels, ports 1-65535; an empty table
// is legal (publish then refuses with 400). Per-route annotations are
// normalized per appName (contract #5): if any line of a service carries
// annotations, every line of that service gets the same set. Any replacement
// bumps the desired revision — an undeployed row stays undeployed regardless
// (status order).
func (h *Handler) ReplaceRoutes(c *gin.Context) {
	row := h.findHost(c)
	if row == nil {
		return
	}
	if !h.authorize(c, "update", row.Cluster, row.Namespace) {
		return
	}
	var req replaceRoutesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	seenPaths := make(map[string]bool, len(req.Routes))
	for i, r := range req.Routes {
		switch {
		case len(r.Path) < 1 || r.Path[0] != '/':
			h.fail(c, http.StatusBadRequest, nil, fmt.Sprintf("routes[%d].path must start with \"/\"", i))
			return
		case seenPaths[r.Path]:
			h.fail(c, http.StatusBadRequest, nil, "duplicate route path: "+r.Path)
			return
		case !validateDNS1123Label(r.AppName, 63):
			h.fail(c, http.StatusBadRequest, nil, fmt.Sprintf("routes[%d].appName must be a DNS-1123 label of at most 63 characters", i))
			return
		case r.Port < 1 || r.Port > 65535:
			h.fail(c, http.StatusBadRequest, nil, fmt.Sprintf("routes[%d].port must be between 1 and 65535", i))
			return
		}
		seenPaths[r.Path] = true
	}
	if !normalizeRouteAnnotations(req.Routes) {
		h.fail(c, http.StatusBadRequest, nil, "routes of the same service carry inconsistent annotations")
		return
	}
	allowed, ok := h.catalogAllowlist(c)
	if !ok {
		return
	}
	family := h.controllerFamily(c.Request.Context(), row.Cluster, row.IngressClass)
	for i := range req.Routes {
		if !h.checkAnnotationKeys(c, row, allowed, family, req.Routes[i].Annotations) {
			return
		}
	}
	data, err := json.Marshal(req.Routes)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	row.Routes = store.JSONField(data)
	h.mutateHostSpec(c, row, "routes", store.JSONField(data))
}

// normalizeRouteAnnotations unifies the annotation set of every route of one
// appName; false means two lines of one service carry different sets.
func normalizeRouteAnnotations(routes []store.Route) bool {
	first := map[string]int{}
	for i := range routes {
		if len(routes[i].Annotations) == 0 {
			continue
		}
		j, seen := first[routes[i].AppName]
		if !seen {
			first[routes[i].AppName] = i
			continue
		}
		if !maps.Equal(routes[i].Annotations, routes[j].Annotations) {
			return false
		}
	}
	for i := range routes {
		if j, ok := first[routes[i].AppName]; ok {
			routes[i].Annotations = routes[j].Annotations
		}
	}
	return true
}

// checkAnnotationKeys verifies one annotation set against the allowlist: the
// key must target the host's ingress class by name or by the class
// controller's family; an empty family (unresolvable controller) falls back
// to the name match only. The response is written on failure.
func (h *Handler) checkAnnotationKeys(c *gin.Context, row *store.Host, allowed map[string]string, family string, annotations map[string]string) bool {
	if len(annotations) == 0 {
		return true
	}
	for key := range annotations {
		class, ok := allowed[key]
		switch {
		case !ok:
			h.fail(c, http.StatusBadRequest, nil, "annotation key not in catalog: "+key)
			return false
		case class != row.IngressClass && (family == "" || class != family):
			h.fail(c, http.StatusBadRequest, nil, "annotation key "+key+" does not apply to ingress class "+row.IngressClass)
			return false
		}
	}
	return true
}

// ReplaceAnnotations (H5) swaps the host's whole annotation set: every key
// must exist in the catalog and apply to the host's ingress class, matched
// by name or by controller family.
func (h *Handler) ReplaceAnnotations(c *gin.Context) {
	row := h.findHost(c)
	if row == nil {
		return
	}
	if !h.authorize(c, "update", row.Cluster, row.Namespace) {
		return
	}
	var req replaceAnnotationsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	allowed, ok := h.catalogAllowlist(c)
	if !ok {
		return
	}
	family := h.controllerFamily(c.Request.Context(), row.Cluster, row.IngressClass)
	if !h.checkAnnotationKeys(c, row, allowed, family, req.Annotations) {
		return
	}
	if req.Annotations == nil {
		req.Annotations = map[string]string{}
	}
	data, err := json.Marshal(req.Annotations)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	row.Annotations = store.JSONField(data)
	h.mutateHostSpec(c, row, "annotations", store.JSONField(data))
}

// catalogAllowlist maps each catalog key to its ingress class restriction;
// false means the listing failed and the response is written.
func (h *Handler) catalogAllowlist(c *gin.Context) (map[string]string, bool) {
	catalog, err := h.store.ListAnnotationKeys()
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return nil, false
	}
	allowed := make(map[string]string, len(catalog))
	for i := range catalog {
		allowed[catalog[i].Key] = catalog[i].IngressClass
	}
	return allowed, true
}

// mutateHostSpec persists one spec column together with the desired-revision
// bump in one transaction, records the update audit and answers with the
// refreshed overview.
func (h *Handler) mutateHostSpec(c *gin.Context, row *store.Host, column string, value interface{}) {
	txErr := h.store.DB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&store.Host{}).Where("id = ?", row.ID).Update(column, value).Error; err != nil {
			return err
		}
		return store.BumpDesiredRevTx(tx, row.ID)
	})
	if txErr != nil {
		if isConcurrentWriteError(txErr) {
			h.fail(c, http.StatusConflict, nil, "network host has been modified concurrently, refresh and retry")
			return
		}
		h.fail(c, http.StatusInternalServerError, txErr, "")
		return
	}
	row.DesiredRev++
	h.recordAudit(h.requestIdentity(c), store.HistoryEntry{
		Cluster: row.Cluster, ResourceType: authzResource, ResourceName: row.Host,
		Namespace: row.Namespace, Operation: "update",
	})
	c.JSON(http.StatusOK, h.buildOverview(c.Request.Context(), row, newClusterClients(c.Request.Context(), h.deps)))
}
