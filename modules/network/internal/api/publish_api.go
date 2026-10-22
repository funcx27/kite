package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/network/internal/render"
	"github.com/funcx27/kite/modules/network/internal/store"
)

// badRequestError marks a failed request-level check (§3.4) so callers answer
// 400 instead of 500.
type badRequestError struct{ msg string }

func (e *badRequestError) Error() string { return e.msg }

// forbiddenError marks a failed per-object permission check (403).
type forbiddenError struct{ msg string }

func (e *forbiddenError) Error() string { return e.msg }

// ownershipError marks a same-name cluster object the module does not manage
// (missing/mismatched kite.network/host label) — never silently taken over.
type ownershipError struct{ key string }

func (e *ownershipError) Error() string {
	return "cluster object " + e.key + " already exists and is not managed by this network host"
}

// PublishHost (H6) converges the cluster onto the host row (design §4.2):
// validate, ensure the space namespace, apply every desired object, upsert
// (or, with the certificate removed, delete) the TLS secret, then flip the
// row to published. Any step's failure lands in last_error and answers with
// the mapped status; the cluster may sit partially applied until a retry.
func (h *Handler) PublishHost(c *gin.Context) {
	row := h.findHost(c)
	if row == nil {
		return
	}
	verb := "update"
	if row.PublishedAt == nil {
		verb = "create"
	}
	if !h.authorize(c, verb, row.Cluster, row.Namespace) {
		return
	}
	id := h.requestIdentity(c)
	ctx := c.Request.Context()
	cli, err := h.deps.ResolveCluster(ctx, row.Cluster)
	if err != nil {
		h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+row.Cluster)
		return
	}
	desired, err := h.publishValidate(ctx, row, cli)
	if err == nil {
		err = h.publishApply(ctx, id, cli, row, desired)
	}
	if err != nil {
		h.failPublish(c, row, id, err)
		return
	}
	if err := h.store.DB().Model(&store.Host{}).Where("id = ?", row.ID).Updates(map[string]interface{}{
		"published_at": time.Now(), "applied_rev": row.DesiredRev,
		"last_error": "", "cert_synced": true,
	}).Error; err != nil {
		h.failPublish(c, row, id, fmt.Errorf("record publish state: %w", err))
		return
	}
	h.recordAudit(id, store.HistoryEntry{
		Cluster: row.Cluster, ResourceType: authzResource, ResourceName: row.Host,
		Namespace: row.Namespace, Operation: "publish", YAML: ingressesJSON(desired),
	})
	fresh, err := h.store.GetHost(row.Cluster, row.Host)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.JSON(http.StatusOK, h.buildOverview(ctx, fresh, newClusterClients(ctx, h.deps)))
}

// publishValidate runs §4.2 step 1's checks and renders the desired objects.
func (h *Handler) publishValidate(ctx context.Context, row *store.Host, cli client.Client) ([]*networkingv1.Ingress, error) {
	var routes []store.Route
	if err := row.Routes.Unmarshal(&routes); err != nil {
		return nil, fmt.Errorf("decode stored routes of %s: %w", row.Host, err)
	}
	if len(routes) == 0 {
		return nil, &badRequestError{msg: "network host has no routes to publish"}
	}
	classes, err := listIngressClasses(ctx, cli)
	if err != nil {
		return nil, fmt.Errorf("list ingress classes of %s: %w", row.Cluster, err)
	}
	class, ok := findIngressClass(classes, row.IngressClass)
	if !ok {
		return nil, &badRequestError{msg: "ingress class " + row.IngressClass + " not found in cluster " + row.Cluster}
	}
	if row.TLSCertificate != "" {
		if _, _, _, err := h.deps.CertificateData(row.TLSCertificate); err != nil {
			return nil, &badRequestError{msg: "certificate " + row.TLSCertificate + " not found"}
		}
	}
	return render.DesiredIngresses(row, render.Options{ControllerFamily: render.ControllerFamily(class.Controller)})
}

// publishApply runs §4.2 steps 2-5.
func (h *Handler) publishApply(ctx context.Context, id identity, cli client.Client, row *store.Host, desired []*networkingv1.Ingress) error {
	if err := h.ensureNamespace(ctx, id, cli, row.Cluster, row.Namespace); err != nil {
		return err
	}
	for _, ing := range desired {
		if err := h.applyIngress(ctx, id, cli, row, ing); err != nil {
			return err
		}
	}
	if err := h.pruneIngresses(ctx, id, cli, row, desired); err != nil {
		return err
	}
	return h.ensureTLSSecret(ctx, id, cli, row)
}

// pruneIngresses deletes the host's managed objects the desired set no longer
// names (a service group removed from the route table or stripped of its
// annotations): apply only converges desired objects, so a leftover would keep
// serving stale backends and count as drift forever.
func (h *Handler) pruneIngresses(ctx context.Context, id identity, cli client.Client, row *store.Host, desired []*networkingv1.Ingress) error {
	live, err := listHostIngresses(ctx, cli, row.Namespace, row.Host)
	if err != nil {
		return fmt.Errorf("list ingresses in %s: %w", row.Namespace, err)
	}
	keep := make(map[string]bool, len(desired))
	for _, ing := range desired {
		keep[ing.Name] = true
	}
	for _, ing := range live {
		if keep[ing.Name] {
			continue
		}
		if !id.authorize(authzResource, "delete", row.Cluster, row.Namespace) {
			return &forbiddenError{msg: fmt.Sprintf("user does not have permission to delete %s in namespace %s on cluster %s", authzResource, row.Namespace, row.Cluster)}
		}
		delErr := cli.Delete(ctx, ing)
		h.recordOperation(id, store.HistoryEntry{
			Cluster: row.Cluster, ResourceType: authzResource, ResourceName: ing.Name,
			Namespace: row.Namespace, Operation: "delete",
		}, delErr == nil || apierrors.IsNotFound(delErr), errString(delErr))
		if delErr != nil && !apierrors.IsNotFound(delErr) {
			return fmt.Errorf("delete ingress %s/%s: %w", ing.Namespace, ing.Name, delErr)
		}
	}
	return nil
}

// ensureNamespace guarantees the space namespace exists with the space marker;
// an existing namespace without the marker is adopted label-only (mirrors the
// application module's spaces_api).
func (h *Handler) ensureNamespace(ctx context.Context, id identity, cli client.Client, cluster, namespace string) error {
	ns := &corev1.Namespace{}
	err := cli.Get(ctx, client.ObjectKey{Name: namespace}, ns)
	switch {
	case err == nil:
		if ns.Labels[render.LabelSpace] == "true" {
			return nil
		}
		if !id.authorize(namespaceResource, "update", cluster, "") {
			return &forbiddenError{msg: fmt.Sprintf("user does not have permission to update namespace %s on cluster %s", namespace, cluster)}
		}
		labels := ns.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		labels[render.LabelSpace] = "true"
		ns.Labels = labels
		if err := cli.Update(ctx, ns); err != nil {
			return fmt.Errorf("adopt namespace %s: %w", namespace, err)
		}
		h.recordAudit(id, store.HistoryEntry{
			Cluster: cluster, ResourceType: namespaceResource, ResourceName: namespace, Operation: "update",
		})
		return nil
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("get namespace %s: %w", namespace, err)
	}
	if !id.authorize(namespaceResource, "create", cluster, "") {
		return &forbiddenError{msg: fmt.Sprintf("user does not have permission to create namespace %s on cluster %s", namespace, cluster)}
	}
	ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   namespace,
			Labels: map[string]string{render.LabelSpace: "true"},
		},
	}
	if err := cli.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", namespace, err)
	}
	h.recordAudit(id, store.HistoryEntry{
		Cluster: cluster, ResourceType: namespaceResource, ResourceName: namespace, Operation: "create",
	})
	return nil
}

// applyIngress creates or updates the managed Ingress (§4.2 step 4): fetch
// live → ownership check → per-object authorize → Create or resourceVersion
// -checked Update.
func (h *Handler) applyIngress(ctx context.Context, id identity, cli client.Client, row *store.Host, desired *networkingv1.Ingress) error {
	live, err := fetchLiveIngress(ctx, cli, desired.Namespace, desired.Name)
	if err != nil {
		return fmt.Errorf("get ingress %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	verb, action := "create", "create"
	if live != nil {
		if live.Labels[render.LabelHost] != row.Host {
			return &ownershipError{key: fmt.Sprintf("ingress %s/%s", desired.Namespace, desired.Name)}
		}
		verb, action = "update", "update"
		desired.ResourceVersion = live.ResourceVersion
	}
	if !id.authorize(authzResource, verb, row.Cluster, row.Namespace) {
		return &forbiddenError{msg: fmt.Sprintf("user does not have permission to %s %s in namespace %s on cluster %s", verb, authzResource, row.Namespace, row.Cluster)}
	}
	if live == nil {
		err = cli.Create(ctx, desired)
	} else {
		err = cli.Update(ctx, desired)
	}
	h.recordOperation(id, store.HistoryEntry{
		Cluster: row.Cluster, ResourceType: authzResource, ResourceName: desired.Name,
		Namespace: desired.Namespace, Operation: action, YAML: ingressJSON(desired),
	}, err == nil, errString(err))
	if err != nil {
		return fmt.Errorf("%s ingress %s/%s: %w", action, desired.Namespace, desired.Name, err)
	}
	return nil
}

// ensureTLSSecret (§4.2 step 5): with a certificate, upsert the kubernetes.io/
// tls secret carrying the material — a live secret whose cert-hash annotation
// already matches is left untouched (unrotated skip). Without a certificate, a
// leftover managed secret of the host is removed.
func (h *Handler) ensureTLSSecret(ctx context.Context, id identity, cli client.Client, row *store.Host) error {
	if row.TLSCertificate == "" {
		return h.removeManagedSecret(ctx, id, cli, row)
	}
	certPem, keyPem, _, err := h.deps.CertificateData(row.TLSCertificate)
	if err != nil {
		return &badRequestError{msg: "certificate " + row.TLSCertificate + " not found"}
	}
	desired := render.DesiredTLSSecret(row, []byte(certPem), []byte(keyPem))
	live := &corev1.Secret{}
	if err := cli.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: desired.Name}, live); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get secret %s/%s: %w", desired.Namespace, desired.Name, err)
		}
		live = nil
	}
	verb, action := "create", "create"
	if live != nil {
		if live.Labels[render.LabelHost] != row.Host {
			return &ownershipError{key: fmt.Sprintf("secret %s/%s", desired.Namespace, desired.Name)}
		}
		if live.Annotations[render.AnnotationCertHash] == desired.Annotations[render.AnnotationCertHash] {
			return nil
		}
		verb, action = "update", "update"
		desired.ResourceVersion = live.ResourceVersion
	}
	if !id.authorize(secretsResource, verb, row.Cluster, row.Namespace) {
		return &forbiddenError{msg: fmt.Sprintf("user does not have permission to %s %s in namespace %s on cluster %s", verb, secretsResource, row.Namespace, row.Cluster)}
	}
	if live == nil {
		err = cli.Create(ctx, desired)
	} else {
		err = cli.Update(ctx, desired)
	}
	// No YAML on the secret entry: its data is the TLS private key material
	// and must never reach the audit log.
	h.recordOperation(id, store.HistoryEntry{
		Cluster: row.Cluster, ResourceType: secretsResource, ResourceName: desired.Name,
		Namespace: desired.Namespace, Operation: action,
	}, err == nil, errString(err))
	if err != nil {
		return fmt.Errorf("%s secret %s/%s: %w", action, desired.Namespace, desired.Name, err)
	}
	return nil
}

// removeManagedSecret deletes the host's managed TLS secret after the
// certificate was removed; absent or foreign objects are a no-op.
func (h *Handler) removeManagedSecret(ctx context.Context, id identity, cli client.Client, row *store.Host) error {
	name := render.ResourceName(row.Host)
	live := &corev1.Secret{}
	if err := cli.Get(ctx, client.ObjectKey{Namespace: row.Namespace, Name: name}, live); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get secret %s/%s: %w", row.Namespace, name, err)
	}
	if live.Labels[render.LabelHost] != row.Host {
		return nil
	}
	if !id.authorize(secretsResource, "delete", row.Cluster, row.Namespace) {
		return &forbiddenError{msg: fmt.Sprintf("user does not have permission to delete %s in namespace %s on cluster %s", secretsResource, row.Namespace, row.Cluster)}
	}
	if err := cli.Delete(ctx, live); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete secret %s/%s: %w", row.Namespace, name, err)
	}
	h.recordOperation(id, store.HistoryEntry{
		Cluster: row.Cluster, ResourceType: secretsResource, ResourceName: name,
		Namespace: row.Namespace, Operation: "delete",
	}, true, "")
	return nil
}

// SyncCert (H7) re-runs only the publish pipeline's secret step, then marks
// the row's certificate synced again.
func (h *Handler) SyncCert(c *gin.Context) {
	row := h.findHost(c)
	if row == nil {
		return
	}
	if !h.authorize(c, "update", row.Cluster, row.Namespace) {
		return
	}
	if row.TLSCertificate == "" {
		h.fail(c, http.StatusBadRequest, nil, "network host has no certificate to sync")
		return
	}
	id := h.requestIdentity(c)
	ctx := c.Request.Context()
	cli, err := h.deps.ResolveCluster(ctx, row.Cluster)
	if err != nil {
		h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+row.Cluster)
		return
	}
	if err := h.ensureTLSSecret(ctx, id, cli, row); err != nil {
		h.fail(c, classifyError(err), err, "")
		return
	}
	if err := h.store.DB().Model(&store.Host{}).Where("id = ?", row.ID).
		Update("cert_synced", true).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	row.CertSynced = true
	c.JSON(http.StatusOK, h.buildOverview(ctx, row, newClusterClients(ctx, h.deps)))
}

// ingressClassView is one entry of the O1 response (contract #1).
type ingressClassView struct {
	Name       string `json:"name"`
	Controller string `json:"controller"`
}

// ListIngressClasses (O1) returns the target cluster's IngressClasses with
// their controllers, so the frontend can derive the controller family.
func (h *Handler) ListIngressClasses(c *gin.Context) {
	cluster := c.Param("targetCluster")
	ctx := c.Request.Context()
	cli, err := h.deps.ResolveCluster(ctx, cluster)
	if err != nil {
		h.fail(c, http.StatusNotFound, nil, "cluster not found or offline")
		return
	}
	if !h.authorize(c, "get", cluster, "") {
		return
	}
	classes, err := listIngressClasses(ctx, cli)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	out := make([]ingressClassView, 0, len(classes))
	for _, class := range classes {
		out = append(out, ingressClassView{Name: class.Name, Controller: class.Controller})
	}
	c.JSON(http.StatusOK, gin.H{"ingressClasses": out})
}

// ingressClassSummary is one IngressClass's sorted name/controller pair.
type ingressClassSummary struct {
	Name       string
	Controller string
}

// listIngressClasses lists the cluster's IngressClasses, sorted by name.
func listIngressClasses(ctx context.Context, cli client.Client) ([]ingressClassSummary, error) {
	var list networkingv1.IngressClassList
	if err := cli.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list ingress classes: %w", err)
	}
	out := make([]ingressClassSummary, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, ingressClassSummary{Name: list.Items[i].Name, Controller: list.Items[i].Spec.Controller})
	}
	slices.SortFunc(out, func(a, b ingressClassSummary) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// findIngressClass locates a class in the listing.
func findIngressClass(classes []ingressClassSummary, name string) (ingressClassSummary, bool) {
	for _, class := range classes {
		if class.Name == name {
			return class, true
		}
	}
	return ingressClassSummary{}, false
}

// classifyError maps a pipeline error to its HTTP status (§3.4).
func classifyError(err error) int {
	var bad *badRequestError
	var forbidden *forbiddenError
	var owned *ownershipError
	switch {
	case errors.As(err, &bad):
		return http.StatusBadRequest
	case errors.As(err, &forbidden):
		return http.StatusForbidden
	case errors.As(err, &owned):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// failPublish persists the failure onto the row's last_error, records the
// failed publish audit entry and answers with the mapped status.
func (h *Handler) failPublish(c *gin.Context, row *store.Host, id identity, err error) {
	if dbErr := h.store.DB().Model(&store.Host{}).Where("id = ?", row.ID).
		Update("last_error", err.Error()).Error; dbErr != nil {
		err = fmt.Errorf("%v (also failed to record the error on the host: %v)", err, dbErr)
	}
	h.recordOperation(id, store.HistoryEntry{
		Cluster: row.Cluster, ResourceType: authzResource, ResourceName: row.Host,
		Namespace: row.Namespace, Operation: "publish",
	}, false, err.Error())
	h.fail(c, classifyError(err), err, "")
}

// ingressJSON renders an Ingress for the audit trail; it carries no secret
// material.
func ingressJSON(ing *networkingv1.Ingress) string {
	data, err := json.Marshal(ing)
	if err != nil {
		return ""
	}
	return string(data)
}

// ingressesJSON renders a desired object set for the audit trail; it carries
// no secret material.
func ingressesJSON(ings []*networkingv1.Ingress) string {
	data, err := json.Marshal(ings)
	if err != nil {
		return ""
	}
	return string(data)
}
