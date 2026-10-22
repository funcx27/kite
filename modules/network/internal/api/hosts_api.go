package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/network/internal/render"
	"github.com/funcx27/kite/modules/network/internal/store"
)

// Status values of the HostOverview DTO (TECH-DESIGN §3.1).
const (
	statusUndeployed = "undeployed"
	statusError      = "error"
	statusPending    = "pending"
	statusSynced     = "synced"
	statusDrift      = "drift"
)

type tlsCertificateView struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Domains   []string   `json:"domains,omitempty"`
}

type hostRouteView struct {
	Path         string            `json:"path"`
	AppName      string            `json:"appName"`
	AppNamespace string            `json:"appNamespace"`
	Port         int32             `json:"port"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	Issues       []string          `json:"issues,omitempty"`
}

// hostOverview is the DTO of every host read/write response (§3.1): optional
// fields (lbAddress, tlsCertificate, annotations) are omitted when empty.
type hostOverview struct {
	Host           string              `json:"host"`
	Space          string              `json:"space"`
	Cluster        string              `json:"cluster"`
	IngressClass   string              `json:"ingressClass"`
	Status         string              `json:"status"`
	CertSynced     bool                `json:"certSynced"`
	LBAddress      string              `json:"lbAddress,omitempty"`
	TLSCertificate *tlsCertificateView `json:"tlsCertificate,omitempty"`
	Annotations    map[string]string   `json:"annotations,omitempty"`
	Routes         []hostRouteView     `json:"routes"`
	LastError      string              `json:"lastError"`
}

// clusterClients memoizes per-request cluster clients so multi-cluster host
// lists resolve each cluster at most once, the clusters' ingress-class
// families so rows sharing a cluster resolve the family mapping once, and
// each cluster+namespace's Service port sets so route-issue checks read the
// Services once per space instead of once per row.
type clusterClients struct {
	deps     Deps
	ctx      context.Context
	cache    map[string]client.Client
	families map[string]map[string]string
	services map[string]map[string]map[int32]bool
}

func newClusterClients(ctx context.Context, deps Deps) *clusterClients {
	return &clusterClients{
		deps:     deps,
		ctx:      ctx,
		cache:    map[string]client.Client{},
		families: map[string]map[string]string{},
		services: map[string]map[string]map[int32]bool{},
	}
}

func (cc *clusterClients) get(cluster string) (client.Client, bool) {
	if cli, ok := cc.cache[cluster]; ok {
		return cli, cli != nil
	}
	cli, err := cc.deps.ResolveCluster(cc.ctx, cluster)
	if err != nil {
		cli = nil
	}
	cc.cache[cluster] = cli
	return cli, cli != nil
}

// familyOf maps the cluster's ingress classes to their controller families,
// memoized per cluster; classes missing from the mapping (cluster unreadable
// or class deleted) yield "", the family-neutral value.
func (cc *clusterClients) familyOf(cluster, class string) string {
	m, ok := cc.families[cluster]
	if !ok {
		m = map[string]string{}
		if cli, resolvable := cc.get(cluster); resolvable {
			var list networkingv1.IngressClassList
			if err := cli.List(cc.ctx, &list); err == nil {
				for i := range list.Items {
					m[list.Items[i].Name] = render.ControllerFamily(list.Items[i].Spec.Controller)
				}
			}
		}
		cc.families[cluster] = m
	}
	return m[class]
}

// servicePortsOf lists the namespace's Service port sets, memoized per
// cluster+namespace; false when the cluster is unresolvable or the list
// fails (best-effort: the caller leaves route issues unset).
func (cc *clusterClients) servicePortsOf(cluster, namespace string) (map[string]map[int32]bool, bool) {
	key := cluster + "/" + namespace
	if ports, ok := cc.services[key]; ok {
		return ports, true
	}
	cli, resolvable := cc.get(cluster)
	if !resolvable {
		return nil, false
	}
	ports, err := servicePorts(cc.ctx, cli, namespace)
	if err != nil {
		return nil, false
	}
	cc.services[key] = ports
	return ports, true
}

// fetchLiveIngress reads the cluster Ingress; nil without error means absent.
func fetchLiveIngress(ctx context.Context, cli client.Client, namespace, name string) (*networkingv1.Ingress, error) {
	var ing networkingv1.Ingress
	if err := cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &ing); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &ing, nil
}

// listHostIngresses lists the namespace's objects carrying the host's
// management label, name-sorted for deterministic iteration.
func listHostIngresses(ctx context.Context, cli client.Client, namespace, host string) ([]*networkingv1.Ingress, error) {
	var list networkingv1.IngressList
	if err := cli.List(ctx, &list, client.InNamespace(namespace), client.MatchingLabels{render.LabelHost: host}); err != nil {
		return nil, fmt.Errorf("list ingresses in %s: %w", namespace, err)
	}
	out := make([]*networkingv1.Ingress, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, &list.Items[i])
	}
	slices.SortFunc(out, func(a, b *networkingv1.Ingress) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// ListHosts (H1) returns the space's hosts, filtered per row by the ingresses
// get permission on the row's cluster. Published rows with neither pending nor
// error state read the live Ingress to derive synced/drift and the LB address.
func (h *Handler) ListHosts(c *gin.Context) {
	space := c.Param("space")
	if !validateDNS1123Label(space, 63) {
		h.fail(c, http.StatusBadRequest, nil, "space name must be a DNS-1123 label of at most 63 characters")
		return
	}
	rows, err := h.store.ListHosts(space, c.Query("cluster"))
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	clients := newClusterClients(c.Request.Context(), h.deps)
	visible := make([]hostOverview, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		if !h.deps.Authorize(c, authzResource, "get", row.Cluster, space) {
			continue
		}
		visible = append(visible, h.buildOverview(c.Request.Context(), row, clients))
	}
	c.JSON(http.StatusOK, gin.H{"hosts": visible})
}

// buildOverview derives the row's response view. The status follows §2's
// order: undeployed → error → pending → live compare. A synced candidate on
// an unreadable cluster degrades to synced (= matches the last successful
// publish), the documented fallback of design §7 risk 2.
func (h *Handler) buildOverview(ctx context.Context, row *store.Host, clients *clusterClients) hostOverview {
	ov := hostOverview{
		Host:         row.Host,
		Space:        row.Namespace,
		Cluster:      row.Cluster,
		IngressClass: row.IngressClass,
		Status:       statusUndeployed,
		CertSynced:   row.CertSynced,
		Routes:       routeViews(row),
		Annotations:  annotationView(row),
		LastError:    row.LastError,
	}
	if row.TLSCertificate != "" {
		cert := &tlsCertificateView{Name: row.TLSCertificate}
		if expiresAt, err := h.deps.CertificateNotAfter(row.TLSCertificate); err == nil {
			cert.ExpiresAt = &expiresAt
		}
		if domains, err := h.deps.CertificateDomains(row.TLSCertificate); err == nil {
			cert.Domains = domains
		}
		ov.TLSCertificate = cert
	}
	switch {
	case row.PublishedAt == nil:
		return ov
	case row.LastError != "":
		ov.Status = statusError
		return ov
	case row.DesiredRev != row.AppliedRev:
		ov.Status = statusPending
		return ov
	}
	ov.Status = statusSynced
	cli, ok := clients.get(row.Cluster)
	if !ok {
		return ov
	}
	desired, err := render.DesiredIngresses(row, render.Options{ControllerFamily: clients.familyOf(row.Cluster, row.IngressClass)})
	if err != nil {
		return ov
	}
	live, err := listHostIngresses(ctx, cli, row.Namespace, row.Host)
	if err != nil {
		return ov
	}
	if liveDrifts(desired, live) {
		ov.Status = statusDrift
	}
	for _, ing := range live {
		if addr := render.LoadBalancerAddress(ing); addr != "" {
			ov.LBAddress = addr
			break
		}
	}
	h.applyRouteIssues(&ov, clients, live)
	return ov
}

// liveDrifts aligns desired and live by name: any missing object, unequal
// object or leftover historical object the last publish did not clean up
// counts as drift.
func liveDrifts(desired, live []*networkingv1.Ingress) bool {
	liveByName := make(map[string]*networkingv1.Ingress, len(live))
	for _, ing := range live {
		liveByName[ing.Name] = ing
	}
	for _, d := range desired {
		lv, ok := liveByName[d.Name]
		if !ok || !render.Compare(d, lv) {
			return true
		}
		delete(liveByName, d.Name)
	}
	return len(liveByName) > 0
}

// applyRouteIssues fills each route's issues against the namespace's live
// Services (memoized per request) and the live object set's paths;
// best-effort — an unreadable Service list leaves the issues unset.
func (h *Handler) applyRouteIssues(ov *hostOverview, clients *clusterClients, live []*networkingv1.Ingress) {
	ports, ok := clients.servicePortsOf(ov.Cluster, ov.Space)
	if !ok {
		return
	}
	paths := map[string]bool{}
	for _, ing := range live {
		for _, p := range livePaths(ing) {
			paths[p] = true
		}
	}
	for i, rv := range ov.Routes {
		var issues []string
		portSet, ok := ports[rv.AppName]
		switch {
		case !ok:
			issues = append(issues, "service-missing")
		case !portSet[rv.Port]:
			issues = append(issues, "port-missing")
		}
		if !paths[rv.Path] {
			issues = append(issues, "path-drift")
		}
		ov.Routes[i].Issues = issues
	}
}

// livePaths flattens the object's backend paths.
func livePaths(ing *networkingv1.Ingress) []string {
	var paths []string
	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, p := range rule.HTTP.Paths {
			paths = append(paths, p.Path)
		}
	}
	return paths
}

func routeViews(row *store.Host) []hostRouteView {
	var routes []store.Route
	if err := row.Routes.Unmarshal(&routes); err != nil {
		return []hostRouteView{}
	}
	out := make([]hostRouteView, 0, len(routes))
	for _, r := range routes {
		out = append(out, hostRouteView{
			Path: r.Path, AppName: r.AppName, AppNamespace: row.Namespace, Port: r.Port,
			Annotations: r.Annotations,
		})
	}
	return out
}

func annotationView(row *store.Host) map[string]string {
	annotations := map[string]string{}
	if err := row.Annotations.Unmarshal(&annotations); err != nil || len(annotations) == 0 {
		return nil
	}
	return annotations
}

type createHostRequest struct {
	Host           string `json:"host" binding:"required"`
	Cluster        string `json:"cluster" binding:"required"`
	IngressClass   string `json:"ingressClass" binding:"required"`
	TLSCertificate string `json:"tlsCertificate"` // omitted/empty = no TLS
}

// CreateHost (H2) registers a domain: DNS-form host, reachable cluster with
// the requested IngressClass, existing certificate, unique (cluster, host).
func (h *Handler) CreateHost(c *gin.Context) {
	space := c.Param("space")
	if !validateDNS1123Label(space, 63) {
		h.fail(c, http.StatusBadRequest, nil, "space name must be a DNS-1123 label of at most 63 characters")
		return
	}
	var req createHostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	// The canonical (lowercase) form is stored, so uniqueness and the derived
	// resource name are shape-stable.
	req.Host = strings.ToLower(strings.TrimSpace(req.Host))
	req.TLSCertificate = strings.TrimSpace(req.TLSCertificate)
	if !validateDNSHost(req.Host) {
		h.fail(c, http.StatusBadRequest, nil, "host must be a lowercase DNS name of at most 253 characters")
		return
	}
	if !validateDNS1123Label(req.IngressClass, 63) {
		h.fail(c, http.StatusBadRequest, nil, "ingressClass must be a DNS-1123 label of at most 63 characters")
		return
	}
	cli, err := h.deps.ResolveCluster(c.Request.Context(), req.Cluster)
	if err != nil {
		h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+req.Cluster)
		return
	}
	if !h.authorize(c, "create", req.Cluster, space) {
		return
	}
	if !h.checkIngressClass(c, cli, req.Cluster, req.IngressClass) {
		return
	}
	if req.TLSCertificate != "" {
		if _, err := h.deps.CertificateNotAfter(req.TLSCertificate); err != nil {
			h.fail(c, http.StatusBadRequest, nil, "certificate "+req.TLSCertificate+" not found")
			return
		}
	}
	h.insertHostRow(c, space, req)
}

// insertHostRow persists the validated create request, answering the 201.
func (h *Handler) insertHostRow(c *gin.Context, space string, req createHostRequest) {
	row := &store.Host{
		Host: req.Host, Cluster: req.Cluster, Namespace: space,
		IngressClass: req.IngressClass, TLSCertificate: req.TLSCertificate,
		CertSynced: req.TLSCertificate == "",
	}
	if err := h.store.CreateHost(row); err != nil {
		if store.IsUniqueViolation(err) {
			h.fail(c, http.StatusConflict, nil, "network host already exists in cluster "+req.Cluster)
			return
		}
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	h.recordAudit(h.requestIdentity(c), store.HistoryEntry{
		Cluster: row.Cluster, ResourceType: authzResource, ResourceName: row.Host,
		Namespace: space, Operation: "create",
	})
	c.JSON(http.StatusCreated, h.buildOverview(c.Request.Context(), row, newClusterClients(c.Request.Context(), h.deps)))
}

// checkIngressClass verifies the class exists on the cluster, writing the
// response itself; false means the caller must stop.
func (h *Handler) checkIngressClass(c *gin.Context, cli client.Client, cluster, class string) bool {
	classes, err := listIngressClasses(c.Request.Context(), cli)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return false
	}
	if _, ok := findIngressClass(classes, class); ok {
		return true
	}
	h.fail(c, http.StatusBadRequest, nil, "ingress class "+class+" not found in cluster "+cluster)
	return false
}

// updateHostSettingsRequest: omitted fields keep their stored value; a
// tlsCertificate of JSON null (or "") removes the certificate.
type updateHostSettingsRequest struct {
	IngressClass   string          `json:"ingressClass,omitempty"`
	TLSCertificate json.RawMessage `json:"tlsCertificate,omitempty"`
}

// UpdateHostSettings (H3): a class change bumps the desired revision (the
// Ingress spec changed); a certificate change only clears CertSynced (the
// referenced secret name is unchanged, design §8/D4). The cluster is
// immutable.
func (h *Handler) UpdateHostSettings(c *gin.Context) {
	row := h.findHost(c)
	if row == nil {
		return
	}
	if !h.authorize(c, "update", row.Cluster, row.Namespace) {
		return
	}
	var req updateHostSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	newCert, ok := h.parseCertificateField(c, req.TLSCertificate)
	if !ok {
		return
	}
	updates := map[string]interface{}{}
	if req.IngressClass != "" && req.IngressClass != row.IngressClass {
		cli, err := h.deps.ResolveCluster(c.Request.Context(), row.Cluster)
		if err != nil {
			h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+row.Cluster)
			return
		}
		if !h.checkIngressClass(c, cli, row.Cluster, req.IngressClass) {
			return
		}
		updates["ingress_class"] = req.IngressClass
	}
	if newCert != nil && *newCert != row.TLSCertificate {
		updates["tls_certificate"] = *newCert
		updates["cert_synced"] = false
	}
	if len(updates) == 0 {
		c.JSON(http.StatusOK, h.buildOverview(c.Request.Context(), row, newClusterClients(c.Request.Context(), h.deps)))
		return
	}
	h.applyHostSettings(c, row, updates)
}

// applyHostSettings persists the settings columns (bumping the desired
// revision when the class changed), records the update audit and answers with
// the refreshed overview.
func (h *Handler) applyHostSettings(c *gin.Context, row *store.Host, updates map[string]interface{}) {
	txErr := h.store.DB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&store.Host{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
			return err
		}
		if _, changed := updates["ingress_class"]; changed {
			return store.BumpDesiredRevTx(tx, row.ID)
		}
		return nil
	})
	if txErr != nil {
		if isConcurrentWriteError(txErr) {
			h.fail(c, http.StatusConflict, nil, "network host has been modified concurrently, refresh and retry")
			return
		}
		h.fail(c, http.StatusInternalServerError, txErr, "")
		return
	}
	if class, changed := updates["ingress_class"]; changed {
		row.IngressClass = class.(string)
		row.DesiredRev++
	}
	if cert, changed := updates["tls_certificate"]; changed {
		row.TLSCertificate = cert.(string)
		row.CertSynced = false
	}
	h.recordAudit(h.requestIdentity(c), store.HistoryEntry{
		Cluster: row.Cluster, ResourceType: authzResource, ResourceName: row.Host,
		Namespace: row.Namespace, Operation: "update",
	})
	c.JSON(http.StatusOK, h.buildOverview(c.Request.Context(), row, newClusterClients(c.Request.Context(), h.deps)))
}

// parseCertificateField interprets the tlsCertificate JSON field: nil keeps
// the stored value, null (or "") clears it, a name sets it (and must exist).
// The returned pointer is nil for "unchanged"; false means the response is
// already written.
func (h *Handler) parseCertificateField(c *gin.Context, raw json.RawMessage) (*string, bool) {
	if raw == nil {
		return nil, true
	}
	name := ""
	if string(raw) != "null" {
		if err := json.Unmarshal(raw, &name); err != nil {
			h.fail(c, http.StatusBadRequest, nil, "tlsCertificate must be a certificate name or null")
			return nil, false
		}
	}
	name = strings.TrimSpace(name)
	if name != "" {
		if _, err := h.deps.CertificateNotAfter(name); err != nil {
			h.fail(c, http.StatusBadRequest, nil, "certificate "+name+" not found")
			return nil, false
		}
	}
	return &name, true
}

// offlineFailure is one failed deletion of the H8 partial-failure response.
type offlineFailure struct {
	Resource string `json:"resource"`
	Error    string `json:"error"`
}

// OfflineHost (H8) takes the host down. With scope=all (the default) the
// managed cluster objects are deleted and the registry row is removed; with
// scope=cluster only the cluster objects are deleted and the row is reset to
// undeployed (contract #6). NotFound objects count as already gone; a foreign
// same-name object is not ours and is skipped. Any real failure keeps the row
// (409) for a retry; an unresolvable cluster counts everything as gone —
// otherwise a removed cluster would strand its hosts online forever.
func (h *Handler) OfflineHost(c *gin.Context) {
	row := h.findHost(c)
	if row == nil {
		return
	}
	if !h.authorize(c, "delete", row.Cluster, row.Namespace) {
		return
	}
	scope := c.DefaultQuery("scope", "all")
	if scope != "all" && scope != "cluster" {
		h.fail(c, http.StatusBadRequest, nil, "scope must be \"cluster\" or \"all\"")
		return
	}
	id := h.requestIdentity(c)
	ctx := c.Request.Context()
	var failed []offlineFailure
	if cli, err := h.deps.ResolveCluster(ctx, row.Cluster); err == nil {
		for _, resource := range []string{authzResource, secretsResource} {
			f, err := h.offlineDeleteLabeled(ctx, id, cli, row, resource)
			if err != nil {
				h.fail(c, http.StatusInternalServerError, err, "")
				return
			}
			failed = append(failed, f...)
		}
	}
	if len(failed) > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "network host offline incomplete, host kept for retry",
			"failed": failed,
		})
		return
	}
	if scope == "cluster" {
		h.offlineKeepRow(c, row)
		return
	}
	if _, err := h.store.DeleteHost(row.Cluster, row.Host); err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "network host " + row.Host + " offline"})
}

// offlineKeepRow resets the row to undeployed after its cluster objects were
// removed, answering with the refreshed overview. CertSynced returns to the
// create-time value: the TLS secret was just deleted, so a row with a
// certificate is no longer synced.
func (h *Handler) offlineKeepRow(c *gin.Context, row *store.Host) {
	if err := h.store.DB().Model(&store.Host{}).Where("id = ?", row.ID).Updates(map[string]interface{}{
		"published_at": nil, "applied_rev": 0, "last_error": "",
		"cert_synced": row.TLSCertificate == "",
	}).Error; err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	fresh, err := h.store.GetHost(row.Cluster, row.Host)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.JSON(http.StatusOK, h.buildOverview(c.Request.Context(), fresh, newClusterClients(c.Request.Context(), h.deps)))
}

// offlineDeleteLabeled deletes every object of the resource carrying the
// host's management label; a listing failure aborts (returned as error),
// per-object failures come back for the 409 body.
func (h *Handler) offlineDeleteLabeled(ctx context.Context, id identity, cli client.Client, row *store.Host, resource string) ([]offlineFailure, error) {
	items, err := listHostObjects(ctx, cli, row, resource)
	if err != nil {
		return nil, fmt.Errorf("list %s in %s: %w", resource, row.Namespace, err)
	}
	var failed []offlineFailure
	for _, item := range items {
		if f := h.offlineDeleteOne(ctx, id, cli, row, resource, item); f != nil {
			failed = append(failed, *f)
		}
	}
	return failed, nil
}

// listHostObjects lists the namespace's objects of the resource carrying the
// host's management label.
func listHostObjects(ctx context.Context, cli client.Client, row *store.Host, resource string) ([]client.Object, error) {
	opts := []client.ListOption{client.InNamespace(row.Namespace), client.MatchingLabels{render.LabelHost: row.Host}}
	switch resource {
	case authzResource:
		var list networkingv1.IngressList
		if err := cli.List(ctx, &list, opts...); err != nil {
			return nil, err
		}
		items := make([]client.Object, 0, len(list.Items))
		for i := range list.Items {
			items = append(items, &list.Items[i])
		}
		return items, nil
	default:
		var list corev1.SecretList
		if err := cli.List(ctx, &list, opts...); err != nil {
			return nil, err
		}
		items := make([]client.Object, 0, len(list.Items))
		for i := range list.Items {
			items = append(items, &list.Items[i])
		}
		return items, nil
	}
}

// offlineDeleteOne deletes one managed object; nil means success (including
// already-absent or foreign).
func (h *Handler) offlineDeleteOne(ctx context.Context, id identity, cli client.Client, row *store.Host, resource string, obj client.Object) *offlineFailure {
	key := resource + " " + row.Namespace + "/" + obj.GetName()
	if obj.GetLabels()[render.LabelHost] != row.Host {
		return nil
	}
	if !id.authorize(resource, "delete", row.Cluster, row.Namespace) {
		return &offlineFailure{Resource: key, Error: "user does not have permission to delete " + resource + " in namespace " + row.Namespace + " on cluster " + row.Cluster}
	}
	delErr := cli.Delete(ctx, obj)
	h.recordOperation(id, store.HistoryEntry{
		Cluster: row.Cluster, ResourceType: resource, ResourceName: obj.GetName(),
		Namespace: row.Namespace, Operation: "delete",
	}, delErr == nil || apierrors.IsNotFound(delErr), errString(delErr))
	if delErr != nil && !apierrors.IsNotFound(delErr) {
		return &offlineFailure{Resource: key, Error: delErr.Error()}
	}
	return nil
}

// validateDNS1123Label accepts an RFC 1123 label (lowercase alphanumerics and
// dashes, starting and ending with an alphanumeric).
func validateDNS1123Label(value string, maxLength int) bool {
	if len(value) < 1 || len(value) > maxLength {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0 && i < len(value)-1:
		default:
			return false
		}
	}
	return true
}

// validateDNSHost accepts a lowercase dot-separated DNS name (an FQDN shape,
// at most 253 characters), each label a DNS-1123 label.
func validateDNSHost(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !validateDNS1123Label(label, 63) {
			return false
		}
	}
	return true
}
