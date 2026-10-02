package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/funcx27/kite/modules/application/internal/runtime"
	"github.com/funcx27/kite/modules/application/internal/store"
)

// GetResources serves the aggregated runtime view (design §10). Permission is
// enforced per section against the section's real resource; sections the
// caller cannot read carry unauthorized=true and no data.
func (h *Handler) GetResources(c *gin.Context) {
	app, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	cli, err := h.deps.ResolveCluster(c.Request.Context(), cfg.Cluster)
	if err != nil {
		h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+cfg.Cluster)
		return
	}
	manifests, err := store.DecodeManifests(cfg.Manifests)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	authz := runtime.Authz(func(resource string) bool {
		return h.deps.Authorize(c, resource, "get", cfg.Cluster, cfg.Namespace)
	})
	view, err := runtime.View(c.Request.Context(), cli, app.Name, cfg.Namespace, manifests, authz)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.JSON(http.StatusOK, view)
}

// RestartConfig rolls the app's workload (design §10 应用重启): the pod
// template is stamped with kite.kubernetes.io/restartedAt, the host restart
// mechanism. RBAC maps to update on the workload's real resource.
func (h *Handler) RestartConfig(c *gin.Context) {
	app, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	cli, err := h.deps.ResolveCluster(c.Request.Context(), cfg.Cluster)
	if err != nil {
		h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+cfg.Cluster)
		return
	}
	manifests, err := store.DecodeManifests(cfg.Manifests)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	wl := runtime.WorkloadOf(app.Name, manifests)
	if wl == nil {
		h.fail(c, http.StatusBadRequest, nil, runtime.ErrNoWorkload.Error())
		return
	}
	resource := kindResource(wl.GetKind())
	if !h.deps.Authorize(c, resource, "update", cfg.Cluster, cfg.Namespace) {
		h.forbidden(c, "update", resource, cfg.Cluster, cfg.Namespace)
		return
	}
	live, err := runtime.RestartWorkload(c.Request.Context(), cli, app.Name, cfg.Namespace, manifests)
	h.recordOperation(h.requestIdentity(c), store.HistoryEntry{
		Cluster: cfg.Cluster, ResourceType: resource, ResourceName: wl.GetName(),
		Namespace: cfg.Namespace, Operation: "update",
	}, err == nil, errString(err))
	if err != nil {
		h.failRestart(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "kind": live.GetKind(), "name": live.GetName()})
}

// RestartPod restarts one pod by deleting it so its controller recreates it
// (design §10). RBAC maps to delete on pods; the pod must belong to the
// app's workload.
func (h *Handler) RestartPod(c *gin.Context) {
	h.deletePod(c, false)
}

// ForceDeletePod removes a pod with a zero grace period, for pods stuck in
// Terminating. Same RBAC and ownership checks as RestartPod.
func (h *Handler) ForceDeletePod(c *gin.Context) {
	h.deletePod(c, true)
}

func (h *Handler) deletePod(c *gin.Context, force bool) {
	app, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	if !h.deps.Authorize(c, "pods", "delete", cfg.Cluster, cfg.Namespace) {
		h.forbidden(c, "delete", "pods", cfg.Cluster, cfg.Namespace)
		return
	}
	cli, err := h.deps.ResolveCluster(c.Request.Context(), cfg.Cluster)
	if err != nil {
		h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+cfg.Cluster)
		return
	}
	manifests, err := store.DecodeManifests(cfg.Manifests)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	podName := c.Param("pod")
	if force {
		err = runtime.ForceDeletePod(c.Request.Context(), cli, app.Name, cfg.Namespace, manifests, podName)
	} else {
		err = runtime.RestartPod(c.Request.Context(), cli, app.Name, cfg.Namespace, manifests, podName)
	}
	h.recordOperation(h.requestIdentity(c), store.HistoryEntry{
		Cluster: cfg.Cluster, ResourceType: "pods", ResourceName: podName,
		Namespace: cfg.Namespace, Operation: "delete",
	}, err == nil, errString(err))
	if err != nil {
		h.failRestart(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "pod": podName})
}

// forbidden writes the same 403 shape the per-document operations use.
func (h *Handler) forbidden(c *gin.Context, verb, resource, cluster, namespace string) {
	h.fail(c, http.StatusForbidden, nil, fmt.Sprintf(
		"user does not have permission to %s %s in namespace %s on cluster %s", verb, resource, namespace, cluster))
}

// failRestart maps restart errors to statuses: missing workload/pod are
// client-visible 4xx, cluster-side API failures surface as their cause.
func (h *Handler) failRestart(c *gin.Context, err error) {
	switch {
	case errors.Is(err, runtime.ErrNoWorkload):
		h.fail(c, http.StatusBadRequest, nil, err.Error())
	case errors.Is(err, runtime.ErrPodNotFound):
		h.fail(c, http.StatusNotFound, nil, err.Error())
	case apierrors.IsNotFound(err):
		h.fail(c, http.StatusNotFound, nil, "workload is not deployed on the cluster")
	case apierrors.IsForbidden(err):
		h.fail(c, http.StatusForbidden, nil, err.Error())
	default:
		h.fail(c, http.StatusInternalServerError, err, "")
	}
}
