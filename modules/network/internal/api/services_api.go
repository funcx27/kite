package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// servicePortView is one port of a listed Service (contract #2).
type servicePortView struct {
	Name string `json:"name"`
	Port int32  `json:"port"`
}

// serviceView is one Service of the namespace listing (contract #2).
type serviceView struct {
	Name  string            `json:"name"`
	Ports []servicePortView `json:"ports"`
}

// ListServices lists the namespace's Services, name-sorted, for the route
// target picker.
func (h *Handler) ListServices(c *gin.Context) {
	cluster := c.Param("targetCluster")
	namespace := c.Param("namespace")
	if !validateDNS1123Label(namespace, 63) {
		h.fail(c, http.StatusBadRequest, nil, "namespace must be a DNS-1123 label of at most 63 characters")
		return
	}
	cli, err := h.deps.ResolveCluster(c.Request.Context(), cluster)
	if err != nil {
		h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+cluster)
		return
	}
	// The target-picker read authorizes against the Service it lists, not the
	// module's ingresses surface.
	if !h.deps.Authorize(c, servicesResource, "get", cluster, namespace) {
		h.fail(c, http.StatusForbidden, nil, "user does not have permission to get "+servicesResource+" in namespace "+namespace+" on cluster "+cluster)
		return
	}
	services, err := listNamespaceServices(c.Request.Context(), cli, namespace)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"services": services})
}

// listNamespaceServices reads the namespace's Services into the contract view,
// sorted by name.
func listNamespaceServices(ctx context.Context, cli client.Client, namespace string) ([]serviceView, error) {
	var list corev1.ServiceList
	if err := cli.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list services in %s: %w", namespace, err)
	}
	out := make([]serviceView, 0, len(list.Items))
	for i := range list.Items {
		svc := &list.Items[i]
		ports := make([]servicePortView, 0, len(svc.Spec.Ports))
		for _, p := range svc.Spec.Ports {
			ports = append(ports, servicePortView{Name: p.Name, Port: p.Port})
		}
		out = append(out, serviceView{Name: svc.Name, Ports: ports})
	}
	slices.SortFunc(out, func(a, b serviceView) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// servicePorts maps each Service name in the namespace to its port set, for
// the route issue checks.
func servicePorts(ctx context.Context, cli client.Client, namespace string) (map[string]map[int32]bool, error) {
	services, err := listNamespaceServices(ctx, cli, namespace)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[int32]bool, len(services))
	for _, svc := range services {
		ports := make(map[int32]bool, len(svc.Ports))
		for _, p := range svc.Ports {
			ports[p.Port] = true
		}
		out[svc.Name] = ports
	}
	return out, nil
}
