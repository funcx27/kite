// Package runtime serves the running side of a cluster config (design §10):
// the aggregated runtime view (workload rollup, pods with live metrics,
// services, ingresses, recent warning events) and the restart actions.
package runtime

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/application/internal/render"
	"github.com/funcx27/kite/modules/application/internal/store"
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

// MaxEvents caps the recent warning events of the runtime view (design §10).
const MaxEvents = 20

// Authz reports read permission on a plural resource name in the view's
// cluster/namespace; sections without permission are hidden (design §7).
type Authz func(resource string) bool

// Section is the permission header of one runtime view section; an
// unauthorized section hides its data (design §7 运行视图逐 section 校验).
type Section struct {
	Unauthorized bool `json:"unauthorized"`
}

// Condition is one workload status condition.
type Condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	LastUpdateTime     string `json:"lastUpdateTime,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

// WorkloadSection is the workload rollup; an empty Kind means the config has
// no workload document.
type WorkloadSection struct {
	Section
	Kind       string      `json:"kind,omitempty"`
	Name       string      `json:"name,omitempty"`
	Namespace  string      `json:"namespace,omitempty"`
	Replicas   int64       `json:"replicas"`
	Ready      int64       `json:"ready"`
	Conditions []Condition `json:"conditions,omitempty"`
	// Requests/Limits sum the pod template's containers — the spec the
	// controller deploys with, visible even while pods are pending.
	Requests *ResourceValues `json:"requests,omitempty"`
	Limits   *ResourceValues `json:"limits,omitempty"`
	// Images are the pod template's container images.
	Images []string `json:"images,omitempty"`
}

// ResourceValues is a CPU+memory snapshot as quantity strings, summed across
// a pod's containers.
type ResourceValues struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// PodInfo is one pod line: name / phase / live CPU (millicores) / memory
// (bytes) / restarts / start time / node (design §10).
type PodInfo struct {
	Name      string `json:"name"`
	Phase     string `json:"phase,omitempty"`
	// Status is the kubectl-style display status (waiting/terminated reason
	// over the raw phase, e.g. ImagePullBackOff while phase is Pending).
	Status    string   `json:"status,omitempty"`
	Node      string   `json:"node,omitempty"`
	IP        string   `json:"ip,omitempty"`
	Images    []string `json:"images,omitempty"`
	StartTime string   `json:"startTime,omitempty"`
	Restarts  int64    `json:"restarts"`
	// Containers are the pod's spec.containers names, for the terminal and
	// logs container pickers.
	Containers []string `json:"containers,omitempty"`
	CPU        *int64   `json:"cpu,omitempty"`
	Memory     *int64   `json:"memory,omitempty"`
	// Requests/Limits sum the pod spec's containers — what the pod actually
	// runs with, which may differ from the workload template after drift.
	Requests *ResourceValues `json:"requests,omitempty"`
	Limits   *ResourceValues `json:"limits,omitempty"`
}

// PodsSection lists the workload's pods; MetricsAvailable reports whether the
// cluster served metrics.k8s.io data for this view.
type PodsSection struct {
	Section
	MetricsAvailable bool      `json:"metricsAvailable"`
	Items            []PodInfo `json:"items"`
}

// ServicePort is one port mapping; TargetPort stays a string because a
// Service targetPort may be a name.
type ServicePort struct {
	Name       string `json:"name,omitempty"`
	Port       int64  `json:"port"`
	TargetPort string `json:"targetPort,omitempty"`
	NodePort   int64  `json:"nodePort,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
}

// ServiceInfo is one live Service of the app.
type ServiceInfo struct {
	Name      string        `json:"name"`
	Type      string        `json:"type,omitempty"`
	ClusterIP string        `json:"clusterIP,omitempty"`
	Ports     []ServicePort `json:"ports,omitempty"`
}

// IngressPath is one host/path route of an Ingress rule.
type IngressPath struct {
	Path     string `json:"path,omitempty"`
	PathType string `json:"pathType,omitempty"`
	Service  string `json:"service,omitempty"`
	Port     string `json:"port,omitempty"`
}

// IngressRule is one host of an Ingress.
type IngressRule struct {
	Host  string        `json:"host,omitempty"`
	Paths []IngressPath `json:"paths,omitempty"`
}

// IngressInfo is one live Ingress of the app.
type IngressInfo struct {
	Name        string        `json:"name"`
	Rules       []IngressRule `json:"rules,omitempty"`
	LBAddresses []string      `json:"lbAddresses,omitempty"`
}

// EventInfo is one recent warning event.
type EventInfo struct {
	InvolvedKind  string `json:"involvedKind,omitempty"`
	InvolvedName  string `json:"involvedName,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Message       string `json:"message,omitempty"`
	Count         int64  `json:"count,omitempty"`
	LastTimestamp string `json:"lastTimestamp,omitempty"`
}

// ServicesSection lists the app's live Services.
type ServicesSection struct {
	Section
	Items []ServiceInfo `json:"items"`
}

// IngressesSection lists the app's live Ingresses.
type IngressesSection struct {
	Section
	Items []IngressInfo `json:"items"`
}

// EventsSection lists recent warning events of the workload and its pods.
type EventsSection struct {
	Section
	Items []EventInfo `json:"items"`
}

// RuntimeView is the aggregated runtime view (design §10); every section
// carries its own permission header.
type RuntimeView struct {
	Workload  WorkloadSection  `json:"workload"`
	Pods      PodsSection      `json:"pods"`
	Services  ServicesSection  `json:"services"`
	Ingresses IngressesSection `json:"ingresses"`
	Events    EventsSection    `json:"events"`
}

// View fetches the live state of the config's documents and aggregates it in
// one response. Objects deleted on the cluster are simply absent; read
// failures other than NotFound abort the view.
func View(ctx context.Context, cli client.Client, app, namespace string, manifests []unstructured.Unstructured, authz Authz) (*RuntimeView, error) {
	v := &RuntimeView{
		Pods:      PodsSection{Items: []PodInfo{}},
		Services:  ServicesSection{Items: []ServiceInfo{}},
		Ingresses: IngressesSection{Items: []IngressInfo{}},
		Events:    EventsSection{Items: []EventInfo{}},
	}
	wl := WorkloadOf(app, manifests)
	live, err := fetchLiveDoc(ctx, cli, namespace, wl)
	if err != nil {
		return nil, err
	}
	selector := selectorOf(wl, live)
	v.viewWorkload(wl, live, authz)
	if err := v.viewPods(ctx, cli, namespace, selector, authz); err != nil {
		return nil, err
	}
	if err := v.viewNetwork(ctx, cli, namespace, manifests, authz); err != nil {
		return nil, err
	}
	if err := v.viewEvents(ctx, cli, namespace, wl, authz); err != nil {
		return nil, err
	}
	return v, nil
}

// WorkloadOf returns the app's workload document (design §1: Deployment or
// StatefulSet), or nil when the manifests define none.
func WorkloadOf(app string, manifests []unstructured.Unstructured) *unstructured.Unstructured {
	if i := render.FindWorkload(app, manifests); i >= 0 {
		return &manifests[i]
	}
	return nil
}

// viewWorkload fills the rollup. The live workload is also the selector
// source of the pods section, so View fetches it regardless of this section's
// permission and only the rollup data is withheld here.
func (v *RuntimeView) viewWorkload(wl, live *unstructured.Unstructured, authz Authz) {
	if wl == nil {
		return
	}
	if !authz(workloadResource(wl.GetKind())) {
		v.Workload.Unauthorized = true
		return
	}
	v.Workload.Kind = wl.GetKind()
	v.Workload.Name = wl.GetName()
	v.Workload.Namespace = wl.GetNamespace()
	src := wl
	if live != nil {
		src = live
	}
	v.Workload.Replicas = defaultReplicas(src)
	v.Workload.Requests, v.Workload.Limits = templateResources(src)
	v.Workload.Images = templateImages(src)
	if live != nil {
		v.Workload.Ready = nestedInt64(live.Object, "status", "readyReplicas")
		v.Workload.Conditions = workloadConditions(live)
	}
}

// viewPods lists the pods behind the workload selector and attaches live
// metrics; a failing metrics API degrades to MetricsAvailable=false (kite
// skips pod metrics the same way, see pkg/resources/pod_handler.go:124).
func (v *RuntimeView) viewPods(ctx context.Context, cli client.Client, namespace string, selector map[string]string, authz Authz) error {
	if !authz("pods") {
		v.Pods.Unauthorized = true
		return nil
	}
	if len(selector) == 0 {
		return nil
	}
	pods := &unstructured.UnstructuredList{}
	pods.SetAPIVersion("v1")
	pods.SetKind("PodList")
	if err := cli.List(ctx, pods, client.InNamespace(namespace), client.MatchingLabels(selector)); err != nil {
		return fmt.Errorf("list pods: %w", err)
	}
	usage, metricsOK := podUsageMap(ctx, cli, namespace)
	v.Pods.MetricsAvailable = metricsOK
	for i := range pods.Items {
		v.Pods.Items = append(v.Pods.Items, podInfo(&pods.Items[i], usage))
	}
	sort.Slice(v.Pods.Items, func(i, j int) bool { return v.Pods.Items[i].Name < v.Pods.Items[j].Name })
	return nil
}

// viewNetwork resolves the live state of the config's Service and Ingress
// documents (design §10).
func (v *RuntimeView) viewNetwork(ctx context.Context, cli client.Client, namespace string, manifests []unstructured.Unstructured, authz Authz) error {
	for i := range manifests {
		doc := &manifests[i]
		switch doc.GetKind() {
		case "Service":
			if !authz("services") {
				v.Services.Unauthorized = true
				continue
			}
			live, err := fetchLiveDoc(ctx, cli, namespace, doc)
			if err != nil {
				return err
			}
			if live != nil {
				v.Services.Items = append(v.Services.Items, serviceInfo(live))
			}
		case "Ingress":
			if !authz("ingresses") {
				v.Ingresses.Unauthorized = true
				continue
			}
			live, err := fetchLiveDoc(ctx, cli, namespace, doc)
			if err != nil {
				return err
			}
			if live != nil {
				v.Ingresses.Items = append(v.Ingresses.Items, ingressInfo(live))
			}
		}
	}
	return nil
}

// viewEvents collects recent warning events of the workload and its pods.
// The module's client is the host's informer cache, which only indexes
// spec.nodeName (pkg/kube/client.go:196), so the involved-object filtering
// the design asks for happens client-side instead of via a field selector.
func (v *RuntimeView) viewEvents(ctx context.Context, cli client.Client, namespace string, wl *unstructured.Unstructured, authz Authz) error {
	if !authz("events") {
		v.Events.Unauthorized = true
		v.Events.Items = nil
		return nil
	}
	owned := map[string]bool{}
	for _, p := range v.Pods.Items {
		owned[p.Name] = true
	}
	events := &unstructured.UnstructuredList{}
	events.SetAPIVersion("v1")
	events.SetKind("EventList")
	if err := cli.List(ctx, events, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("list events: %w", err)
	}
	type dated struct {
		info EventInfo
		at   time.Time
	}
	var recent []dated
	for i := range events.Items {
		ev, ok := warningEventOf(&events.Items[i], wl, owned)
		if ok {
			at, _ := time.Parse(time.RFC3339, ev.LastTimestamp)
			recent = append(recent, dated{info: ev, at: at})
		}
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].at.After(recent[j].at) })
	if len(recent) > MaxEvents {
		recent = recent[:MaxEvents]
	}
	for _, d := range recent {
		v.Events.Items = append(v.Events.Items, d.info)
	}
	return nil
}

// fetchLiveDoc reads the live object of a document; a missing object returns
// (nil, nil), other read errors are wrapped.
func fetchLiveDoc(ctx context.Context, cli client.Client, namespace string, tpl *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if tpl == nil {
		return nil, nil
	}
	obj := syncops.BlankDoc(tpl)
	err := cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: tpl.GetName()}, obj)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", store.DocKeyOf(tpl), err)
	}
	return obj, nil
}

// selectorOf returns the workload's pod selector, preferring the live object;
// the pods the view lists are the ones the running workload actually owns.
func selectorOf(wl, live *unstructured.Unstructured) map[string]string {
	if wl == nil {
		return nil
	}
	src := wl
	if live != nil {
		src = live
	}
	m, _, _ := unstructured.NestedStringMap(src.Object, "spec", "selector", "matchLabels")
	return m
}

// workloadResource maps the workload kinds to their plural resource names.
func workloadResource(kind string) string {
	if kind == "StatefulSet" {
		return "statefulsets"
	}
	return "deployments"
}

// defaultReplicas reads spec.replicas with the API server default of 1.
func defaultReplicas(doc *unstructured.Unstructured) int64 {
	n, found, _ := unstructured.NestedInt64(doc.Object, "spec", "replicas")
	if found {
		return n
	}
	return 1
}

func nestedInt64(obj map[string]interface{}, fields ...string) int64 {
	n, found, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	if !found {
		return 0
	}
	return toInt64(n)
}

// toInt64 coerces the number shapes a decoded JSON tree can carry.
func toInt64(v interface{}) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	}
	return 0
}

func nestedString(obj map[string]interface{}, fields ...string) string {
	s, _, _ := unstructured.NestedString(obj, fields...)
	return s
}

func workloadConditions(live *unstructured.Unstructured) []Condition {
	raw, _, _ := unstructured.NestedSlice(live.Object, "status", "conditions")
	out := make([]Condition, 0, len(raw))
	for _, item := range raw {
		m, ok := store.AsMap(item)
		if !ok {
			continue
		}
		out = append(out, Condition{
			Type:               nestedString(m, "type"),
			Status:             nestedString(m, "status"),
			Reason:             nestedString(m, "reason"),
			Message:            nestedString(m, "message"),
			LastUpdateTime:     nestedString(m, "lastUpdateTime"),
			LastTransitionTime: nestedString(m, "lastTransitionTime"),
		})
	}
	return out
}

// podUsage is a pod's summed live CPU (millicores) and memory (bytes), the
// units kite's own pod metrics use (pkg/resources/pod_handler.go:74).
type podUsage struct {
	cpu int64
	mem int64
}

// podUsageMap indexes the namespace's metrics.k8s.io pod usage by "ns/name".
// Any failure (metrics-server absent, API error) yields ok=false.
func podUsageMap(ctx context.Context, cli client.Client, namespace string) (map[string]*podUsage, bool) {
	list := &unstructured.UnstructuredList{}
	list.SetAPIVersion("metrics.k8s.io/v1beta1")
	list.SetKind("PodMetricsList")
	if err := cli.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, false
	}
	out := make(map[string]*podUsage, len(list.Items))
	for i := range list.Items {
		u := &podUsage{}
		containers, _, _ := unstructured.NestedSlice(list.Items[i].Object, "containers")
		for _, c := range containers {
			cm, ok := store.AsMap(c)
			if !ok {
				continue
			}
			usage, _ := store.AsMap(cm["usage"])
			u.cpu += quantityTo(usage["cpu"], true)
			u.mem += quantityTo(usage["memory"], false)
		}
		out[list.Items[i].GetNamespace()+"/"+list.Items[i].GetName()] = u
	}
	return out, true
}

// quantityTo parses a Quantity string into millicores (milli=true) or bytes.
func quantityTo(v interface{}, milli bool) int64 {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	if milli {
		return q.MilliValue()
	}
	return q.Value()
}

func podInfo(pod *unstructured.Unstructured, usage map[string]*podUsage) PodInfo {
	info := PodInfo{
		Name:      pod.GetName(),
		Phase:     nestedString(pod.Object, "status", "phase"),
		Status:    podStatus(pod),
		Node:      nestedString(pod.Object, "spec", "nodeName"),
		IP:        nestedString(pod.Object, "status", "podIP"),
		StartTime: nestedString(pod.Object, "status", "startTime"),
		Restarts:  podRestarts(pod),
	}
	if containers, _, _ := unstructured.NestedSlice(pod.Object, "spec", "containers"); len(containers) > 0 {
		for _, c := range containers {
			if m, ok := store.AsMap(c); ok {
				if name := nestedString(m, "name"); name != "" {
					info.Containers = append(info.Containers, name)
				}
				if image := nestedString(m, "image"); image != "" {
					info.Images = append(info.Images, image)
				}
			}
		}
		info.Requests, info.Limits = sumResources(containers)
	}
	if u, ok := usage[pod.GetNamespace()+"/"+pod.GetName()]; ok {
		cpu, mem := u.cpu, u.mem
		info.CPU, info.Memory = &cpu, &mem
	}
	return info
}

// templateResources sums the workload pod template's container resources —
// the spec the controller deploys with.
func templateResources(src *unstructured.Unstructured) (*ResourceValues, *ResourceValues) {
	containers, _, _ := unstructured.NestedSlice(src.Object, "spec", "template", "spec", "containers")
	return sumResources(containers)
}

// templateImages lists the workload pod template's container images.
func templateImages(src *unstructured.Unstructured) []string {
	containers, _, _ := unstructured.NestedSlice(src.Object, "spec", "template", "spec", "containers")
	var out []string
	for _, c := range containers {
		if m, ok := store.AsMap(c); ok {
			if image := nestedString(m, "image"); image != "" {
				out = append(out, image)
			}
		}
	}
	return out
}

// sumResources totals the requests/limits of a container list; an all-zero
// total returns (nil, nil).
func sumResources(containers []interface{}) (*ResourceValues, *ResourceValues) {
	var reqCPU, reqMem, limCPU, limMem int64
	for _, c := range containers {
		m, ok := store.AsMap(c)
		if !ok {
			continue
		}
		res, ok := store.AsMap(m["resources"])
		if !ok {
			continue
		}
		if req, ok := store.AsMap(res["requests"]); ok {
			reqCPU += quantityTo(req["cpu"], true)
			reqMem += quantityTo(req["memory"], false)
		}
		if lim, ok := store.AsMap(res["limits"]); ok {
			limCPU += quantityTo(lim["cpu"], true)
			limMem += quantityTo(lim["memory"], false)
		}
	}
	req := &ResourceValues{CPU: formatCPU(reqCPU), Memory: formatMemory(reqMem)}
	lim := &ResourceValues{CPU: formatCPU(limCPU), Memory: formatMemory(limMem)}
	if req.CPU == "" && req.Memory == "" && lim.CPU == "" && lim.Memory == "" {
		return nil, nil
	}
	return req, lim
}

// formatCPU renders millicores the kubectl way: whole cores when even, else
// a milli value.
func formatCPU(milli int64) string {
	if milli == 0 {
		return ""
	}
	if milli%1000 == 0 {
		return strconv.FormatInt(milli/1000, 10)
	}
	return strconv.FormatInt(milli, 10) + "m"
}

// formatMemory renders bytes in binary units — Gi when at least 1Gi (exact
// multiples drop the decimal), otherwise Mi — so specs never degrade to Ki.
func formatMemory(bytes int64) string {
	if bytes == 0 {
		return ""
	}
	const mi = int64(1) << 20
	const gi = int64(1) << 30
	if bytes >= gi {
		if bytes%gi == 0 {
			return fmt.Sprintf("%dGi", bytes/gi)
		}
		return fmt.Sprintf("%.1fGi", float64(bytes)/float64(gi))
	}
	if bytes%mi == 0 {
		return fmt.Sprintf("%dMi", bytes/mi)
	}
	return fmt.Sprintf("%.1fMi", float64(bytes)/float64(mi))
}

func podRestarts(pod *unstructured.Unstructured) int64 {
	var total int64
	statuses, _, _ := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
	for _, s := range statuses {
		m, ok := store.AsMap(s)
		if !ok {
			continue
		}
		total += toInt64(m["restartCount"])
	}
	return total
}

func serviceInfo(live *unstructured.Unstructured) ServiceInfo {
	info := ServiceInfo{
		Name:      live.GetName(),
		Type:      nestedString(live.Object, "spec", "type"),
		ClusterIP: nestedString(live.Object, "spec", "clusterIP"),
	}
	ports, _, _ := unstructured.NestedSlice(live.Object, "spec", "ports")
	for _, p := range ports {
		m, ok := store.AsMap(p)
		if !ok {
			continue
		}
		info.Ports = append(info.Ports, ServicePort{
			Name:       nestedString(m, "name"),
			Port:       toInt64(m["port"]),
			TargetPort: portValue(m["targetPort"]),
			NodePort:   toInt64(m["nodePort"]),
			Protocol:   nestedString(m, "protocol"),
		})
	}
	return info
}

// portValue stringifies a port that may be a number or a name.
func portValue(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case int64, int, float64:
		return strconv.FormatInt(toInt64(t), 10)
	}
	return ""
}

func ingressInfo(live *unstructured.Unstructured) IngressInfo {
	info := IngressInfo{Name: live.GetName()}
	rules, _, _ := unstructured.NestedSlice(live.Object, "spec", "rules")
	for _, r := range rules {
		rm, ok := store.AsMap(r)
		if !ok {
			continue
		}
		rule := IngressRule{Host: nestedString(rm, "host")}
		paths, _, _ := unstructured.NestedSlice(rm, "http", "paths")
		for _, p := range paths {
			pm, ok := store.AsMap(p)
			if !ok {
				continue
			}
			rule.Paths = append(rule.Paths, IngressPath{
				Path:     nestedString(pm, "path"),
				PathType: nestedString(pm, "pathType"),
				Service:  nestedString(pm, "backend", "service", "name"),
				Port:     backendPort(pm),
			})
		}
		info.Rules = append(info.Rules, rule)
	}
	lbs, _, _ := unstructured.NestedSlice(live.Object, "status", "loadBalancer", "ingress")
	for _, lb := range lbs {
		m, ok := store.AsMap(lb)
		if !ok {
			continue
		}
		if addr := nestedString(m, "ip") + nestedString(m, "hostname"); addr != "" {
			info.LBAddresses = append(info.LBAddresses, addr)
		}
	}
	return info
}

// backendPort stringifies the backend service port (number or name).
func backendPort(path map[string]interface{}) string {
	backend, ok := store.AsMap(path["backend"])
	if !ok {
		return ""
	}
	svc, ok := store.AsMap(backend["service"])
	if !ok {
		return ""
	}
	port, ok := store.AsMap(svc["port"])
	if !ok {
		return ""
	}
	if n := toInt64(port["number"]); n != 0 {
		return strconv.FormatInt(n, 10)
	}
	return nestedString(port, "name")
}

// warningEventOf projects a corev1 Event the view cares about: Warning type
// on the workload or one of its pods (design §10).
func warningEventOf(ev *unstructured.Unstructured, wl *unstructured.Unstructured, podNames map[string]bool) (EventInfo, bool) {
	if nestedString(ev.Object, "type") != "Warning" {
		return EventInfo{}, false
	}
	kind := nestedString(ev.Object, "involvedObject", "kind")
	name := nestedString(ev.Object, "involvedObject", "name")
	relevant := kind == "Pod" && podNames[name]
	if !relevant && wl != nil {
		relevant = kind == wl.GetKind() && name == wl.GetName()
	}
	if !relevant {
		return EventInfo{}, false
	}
	return EventInfo{
		InvolvedKind:  kind,
		InvolvedName:  name,
		Reason:        nestedString(ev.Object, "reason"),
		Message:       nestedString(ev.Object, "message"),
		Count:         toInt64(ev.Object["count"]),
		LastTimestamp: eventTimestamp(ev),
	}, true
}

// eventTimestamp picks the freshest timestamp an event carries.
func eventTimestamp(ev *unstructured.Unstructured) string {
	for _, field := range [][]string{
		{"lastTimestamp"}, {"eventTime"}, {"firstTimestamp"}, {"metadata", "creationTimestamp"},
	} {
		if s := nestedString(ev.Object, field...); s != "" {
			return s
		}
	}
	return ""
}
