package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/application/internal/render"
	"github.com/funcx27/kite/modules/application/internal/store"
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

// A space is a namespace carrying the kite space marker; its quota lives on a
// kite-managed ResourceQuota inside that namespace — the cluster objects are
// the source of truth, there is no module table.
const (
	spaceQuotaName    = "kite-space-quota"
	namespaceResource = "namespaces"
	quotaResource     = "resourcequotas"
)

// spaceQuotaUpsert is one cluster's row of the PUT body.
type spaceQuotaUpsert struct {
	Cluster string `json:"cluster"`
	CPU     string `json:"cpu"`
	Memory  string `json:"memory"`
}

type upsertSpaceQuotasRequest struct {
	Quotas []spaceQuotaUpsert `json:"quotas"`
}

type spaceQuotaFailure struct {
	Cluster string `json:"cluster"`
	Error   string `json:"error"`
}

type upsertSpaceQuotasResponse struct {
	Succeeded []string            `json:"succeeded"`
	Failed    []spaceQuotaFailure `json:"failed"`
}

// spaceQuotaRow is one cluster's view of the space quota: configured rows
// carry the values, otherwise exactly one of unconfigured/unauthorized/error
// applies.
type spaceQuotaRow struct {
	Cluster      string `json:"cluster"`
	CPU          string `json:"cpu,omitempty"`
	Memory       string `json:"memory,omitempty"`
	UsedCPU      string `json:"usedCpu,omitempty"`
	UsedMemory   string `json:"usedMemory,omitempty"`
	Unconfigured bool   `json:"unconfigured,omitempty"`
	Unauthorized bool   `json:"unauthorized,omitempty"`
	Error        string `json:"error,omitempty"`
}

type spaceQuotasResponse struct {
	Quotas []spaceQuotaRow `json:"quotas"`
}

// UpsertSpaceQuotas creates the space and sets its quota on every requested
// cluster (one upsert endpoint for both). Each cluster is ensured
// independently, so a partial failure leaves the others applied and the whole
// request can be retried as-is.
func (h *Handler) UpsertSpaceQuotas(c *gin.Context) {
	space := c.Param("name")
	var req upsertSpaceQuotasRequest
	if !h.bindSpaceQuotasReq(c, space, &req) {
		return
	}
	id := h.requestIdentity(c)
	ctx := c.Request.Context()
	resp := upsertSpaceQuotasResponse{
		Succeeded: make([]string, 0, len(req.Quotas)),
		Failed:    make([]spaceQuotaFailure, 0),
	}
	for _, q := range req.Quotas {
		if err := h.ensureSpaceQuota(ctx, id, q.Cluster, space, q.CPU, q.Memory); err != nil {
			resp.Failed = append(resp.Failed, spaceQuotaFailure{Cluster: q.Cluster, Error: err.Error()})
			continue
		}
		resp.Succeeded = append(resp.Succeeded, q.Cluster)
	}
	c.JSON(http.StatusOK, resp)
}

// bindSpaceQuotasReq parses and validates the PUT body; false means the 4xx
// response is already written.
func (h *Handler) bindSpaceQuotasReq(c *gin.Context, space string, req *upsertSpaceQuotasRequest) bool {
	if !render.ValidateDNS1123Label(space, 63) {
		h.fail(c, http.StatusBadRequest, nil, "space name must be a DNS-1123 label of at most 63 characters")
		return false
	}
	if err := c.ShouldBindJSON(req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return false
	}
	if len(req.Quotas) == 0 {
		h.fail(c, http.StatusBadRequest, nil, "quotas must not be empty")
		return false
	}
	for i, q := range req.Quotas {
		switch {
		case q.Cluster == "":
			h.fail(c, http.StatusBadRequest, nil, fmt.Sprintf("quotas[%d].cluster must not be empty", i))
			return false
		case q.CPU == "", q.Memory == "":
			h.fail(c, http.StatusBadRequest, nil, fmt.Sprintf("quotas[%d] (%s): cpu and memory are required", i, q.Cluster))
			return false
		}
		if _, err := resource.ParseQuantity(q.CPU); err != nil {
			h.fail(c, http.StatusBadRequest, nil, fmt.Sprintf("quotas[%d] (%s): cpu must be a valid quantity: %v", i, q.Cluster, err))
			return false
		}
		if _, err := resource.ParseQuantity(q.Memory); err != nil {
			h.fail(c, http.StatusBadRequest, nil, fmt.Sprintf("quotas[%d] (%s): memory must be a valid quantity: %v", i, q.Cluster, err))
			return false
		}
	}
	return true
}

// ensureSpaceQuota runs one cluster's ensure: the space namespace first, then
// the managed quota inside it.
func (h *Handler) ensureSpaceQuota(ctx context.Context, id identity, cluster, space, cpu, memory string) error {
	cli, err := h.deps.ResolveCluster(ctx, cluster)
	if err != nil {
		return fmt.Errorf("resolve cluster: %w", err)
	}
	if err := h.ensureSpaceNamespace(ctx, id, cli, cluster, space); err != nil {
		return err
	}
	return h.applySpaceQuota(ctx, id, cli, cluster, space, cpu, memory)
}

// ensureSpaceNamespace guarantees the space namespace exists with the space
// marker. A namespace created outside kite without the marker is refused,
// never adopted.
func (h *Handler) ensureSpaceNamespace(ctx context.Context, id identity, cli client.Client, cluster, space string) error {
	existing := syncops.BlankDoc(&unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Namespace",
	}})
	existing.SetName(space)
	err := cli.Get(ctx, client.ObjectKey{Name: space}, existing)
	switch {
	case err == nil:
		if existing.GetLabels()[store.LabelKiteSpace] != "true" {
			return fmt.Errorf("namespace %s exists but is not a kite space", space)
		}
		return nil
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("get namespace %s: %w", space, err)
	}
	if !id.authorize(namespaceResource, "create", cluster, "") {
		return fmt.Errorf("user does not have permission to create namespace %s on cluster %s", space, cluster)
	}
	ns := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]interface{}{
			"name":   space,
			"labels": map[string]interface{}{store.LabelKiteSpace: "true"},
		},
	}}
	if err := cli.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", space, err)
	}
	h.recordAudit(id, store.HistoryEntry{
		Cluster: cluster, ResourceType: namespaceResource, ResourceName: space, Operation: "create",
	})
	return nil
}

// applySpaceQuota upserts the space's managed ResourceQuota, all four hard
// keys carrying the requested cpu/memory. A quota without the managed
// annotation is foreign and refused; the update only rewrites spec.hard.
func (h *Handler) applySpaceQuota(ctx context.Context, id identity, cli client.Client, cluster, space, cpu, memory string) error {
	existing, err := getLiveSpaceQuota(ctx, cli, space)
	if err != nil {
		return err
	}
	quota, verb, action := existing, "update", "update"
	if existing == nil {
		quota, verb, action = newSpaceQuota(space), "create", "create"
	} else if existing.GetAnnotations()[store.AnnotationKiteManaged] != "true" {
		return fmt.Errorf("resource quota %s/%s exists but is not managed by kite", space, spaceQuotaName)
	}
	if !id.authorize(quotaResource, verb, cluster, space) {
		return fmt.Errorf("user does not have permission to %s %s in namespace %s on cluster %s", verb, quotaResource, space, cluster)
	}
	quota.Object["spec"] = map[string]interface{}{"hard": map[string]interface{}{
		"cpu":           cpu,
		"memory":        memory,
		"limits.cpu":    cpu,
		"limits.memory": memory,
	}}
	var applyErr error
	if existing == nil {
		applyErr = cli.Create(ctx, quota)
	} else {
		applyErr = cli.Update(ctx, quota)
	}
	h.recordOperation(id, store.HistoryEntry{
		Cluster: cluster, ResourceType: quotaResource, ResourceName: spaceQuotaName,
		Namespace: space, Operation: action, YAML: redactedDoc(quota),
	}, applyErr == nil, errString(applyErr))
	if applyErr != nil {
		return fmt.Errorf("%s resource quota %s/%s: %w", action, space, spaceQuotaName, applyErr)
	}
	return nil
}

// getLiveSpaceQuota reads the space's quota object; nil means absent.
func getLiveSpaceQuota(ctx context.Context, cli client.Client, space string) (*unstructured.Unstructured, error) {
	quota := syncops.BlankDoc(&unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ResourceQuota",
	}})
	quota.SetName(spaceQuotaName)
	quota.SetNamespace(space)
	err := cli.Get(ctx, client.ObjectKey{Namespace: space, Name: spaceQuotaName}, quota)
	switch {
	case err == nil:
		return quota, nil
	case apierrors.IsNotFound(err):
		return nil, nil
	default:
		return nil, fmt.Errorf("get resource quota %s/%s: %w", space, spaceQuotaName, err)
	}
}

// newSpaceQuota builds the managed quota object for the create path.
func newSpaceQuota(space string) *unstructured.Unstructured {
	quota := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ResourceQuota",
		"metadata": map[string]interface{}{
			"name":        spaceQuotaName,
			"namespace":   space,
			"labels":      map[string]interface{}{store.LabelKiteSpace: "true"},
			"annotations": map[string]interface{}{store.AnnotationKiteManaged: "true"},
		},
	}}
	return quota
}

// GetSpaceQuotas reports the space quota of every known cluster: clusters
// without the space namespace are skipped; the rest carry the managed quota's
// hard/used values as raw quantity strings.
func (h *Handler) GetSpaceQuotas(c *gin.Context) {
	space := c.Param("name")
	if !render.ValidateDNS1123Label(space, 63) {
		h.fail(c, http.StatusBadRequest, nil, "space name must be a DNS-1123 label of at most 63 characters")
		return
	}
	id := h.requestIdentity(c)
	ctx := c.Request.Context()
	rows := make([]spaceQuotaRow, 0)
	for _, cluster := range h.deps.ClusterNames() {
		if row, ok := h.clusterSpaceQuota(ctx, id, cluster, space); ok {
			rows = append(rows, row)
		}
	}
	c.JSON(http.StatusOK, spaceQuotasResponse{Quotas: rows})
}

// clusterSpaceQuota builds one cluster's row; ok=false means the cluster
// carries no space and the row is skipped.
func (h *Handler) clusterSpaceQuota(ctx context.Context, id identity, cluster, space string) (spaceQuotaRow, bool) {
	cli, err := h.deps.ResolveCluster(ctx, cluster)
	if err != nil {
		return spaceQuotaRow{Cluster: cluster, Error: fmt.Sprintf("resolve cluster: %v", err)}, true
	}
	ns := syncops.BlankDoc(&unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Namespace",
	}})
	ns.SetName(space)
	if err := cli.Get(ctx, client.ObjectKey{Name: space}, ns); err != nil {
		if !apierrors.IsNotFound(err) {
			return spaceQuotaRow{Cluster: cluster, Error: fmt.Sprintf("get namespace %s: %v", space, err)}, true
		}
		return spaceQuotaRow{}, false
	}
	if ns.GetLabels()[store.LabelKiteSpace] != "true" {
		return spaceQuotaRow{}, false
	}
	if !id.authorize(quotaResource, "get", cluster, space) {
		return spaceQuotaRow{Cluster: cluster, Unauthorized: true}, true
	}
	quota, err := getLiveSpaceQuota(ctx, cli, space)
	if err != nil {
		return spaceQuotaRow{Cluster: cluster, Error: err.Error()}, true
	}
	if quota == nil {
		return spaceQuotaRow{Cluster: cluster, Unconfigured: true}, true
	}
	row := spaceQuotaRow{Cluster: cluster}
	row.CPU, _, _ = unstructured.NestedString(quota.Object, "spec", "hard", "cpu")
	row.Memory, _, _ = unstructured.NestedString(quota.Object, "spec", "hard", "memory")
	row.UsedCPU, _, _ = unstructured.NestedString(quota.Object, "spec", "used", "cpu")
	row.UsedMemory, _, _ = unstructured.NestedString(quota.Object, "spec", "used", "memory")
	return row, true
}
