package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/funcx27/kite/modules/network/internal/render"
)

// ExportHost renders the host's desired objects as a multi-document YAML (---
// separated) for review or out-of-band apply (contract #7).
func (h *Handler) ExportHost(c *gin.Context) {
	row := h.findHost(c)
	if row == nil {
		return
	}
	if !h.authorize(c, "get", row.Cluster, row.Namespace) {
		return
	}
	family := h.controllerFamily(c.Request.Context(), row.Cluster, row.IngressClass)
	desired, err := render.DesiredIngresses(row, render.Options{ControllerFamily: family})
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	var buf strings.Builder
	for _, ing := range desired {
		// The applied objects carry no TypeMeta; the export must stand alone,
		// so each document gets an explicit GVK for kubectl apply / re-import.
		out := ing.DeepCopy()
		out.TypeMeta = metav1.TypeMeta{Kind: "Ingress", APIVersion: networkingv1.SchemeGroupVersion.String()}
		data, err := yaml.Marshal(out)
		if err != nil {
			h.fail(c, http.StatusInternalServerError, err, "")
			return
		}
		buf.WriteString("---\n")
		buf.Write(data)
	}
	c.Data(http.StatusOK, "text/yaml; charset=utf-8", []byte(buf.String()))
}
