package render

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/funcx27/kite/modules/application/internal/store"
)

const managedByValue = "kite"

// Fixed names of the module-generated workload container and config volumes
// (design §8/§9): the form view finds them by these names, foreign ones are
// preserved verbatim.
const (
	containerName    = "app"
	configVolumeName = "config"
	secretVolumeName = "secret"

	configSuffix = "-config"
	secretSuffix = "-secret"

	ingressPathType = "Prefix"
)

func appLabels(app string) map[string]interface{} {
	return map[string]interface{}{
		store.LabelAppName:   app,
		store.LabelManagedBy: managedByValue,
		store.LabelKiteName:  app,
	}
}

// stampLabels merges the identity trio into the document's existing labels.
// It writes through the Object tree rather than SetLabels so values stay
// JSON-shaped and DeepCopy/Normalize on freshly rendered docs never trip
// over typed map[string]string values.
func stampLabels(doc *unstructured.Unstructured, app string) {
	meta, ok := store.AsMap(doc.Object["metadata"])
	if !ok {
		meta = map[string]interface{}{}
		doc.Object["metadata"] = meta
	}
	labels, _ := store.AsMap(meta["labels"])
	if labels == nil {
		labels = map[string]interface{}{}
		meta["labels"] = labels
	}
	for k, v := range appLabels(app) {
		labels[k] = v
	}
}

// Render turns a wizard spec into the full multi-document manifest set
// (design §3/§8/§9): the workload, services grouped by port type, ingress
// rules, the app config map/secret with per-file subPath mounts, plus the
// ExtraYAML documents. Pure computation; every document carries the §5 label
// trio.
func Render(app string, spec AppSpec, namespace string) ([]unstructured.Unstructured, error) {
	if !ValidateDNS1123Label(app, 63) {
		return nil, fmt.Errorf("application name %q must be a DNS-1123 label of at most 63 characters", app)
	}
	spec.Normalize(app)
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("invalid spec: %w", err)
	}
	docs := []unstructured.Unstructured{*buildWorkload(app, spec, namespace)}
	docs = append(docs, buildServiceDocs(app, spec, namespace)...)
	if ing := buildIngressDoc(app, spec, namespace); ing != nil {
		docs = append(docs, *ing)
	}
	docs = append(docs, buildConfigDocs(app, spec, namespace)...)
	extra, err := store.ParseExtraYAML(spec.ExtraYAML)
	if err != nil {
		return nil, fmt.Errorf("parse extraYaml: %w", err)
	}
	for i := range extra {
		stampLabels(&extra[i], app)
		docs = append(docs, extra[i])
	}
	return docs, nil
}

func isWorkloadKind(doc *unstructured.Unstructured) bool {
	return doc.GetKind() == "Deployment" || doc.GetKind() == "StatefulSet"
}

func workloadKind(t string) string {
	if t == WorkloadStatefulSet {
		return "StatefulSet"
	}
	return "Deployment"
}

// portName returns the explicit or auto-generated port name tcp-<port>
// (design §8). A hyphen, not an underscore: container port names and Service
// port names are both DNS-1123 style labels and the API server rejects '_'.
func portName(p PortSpec) string {
	if p.Name != "" {
		return p.Name
	}
	return "tcp-" + strconv.Itoa(int(p.Port))
}

// serviceName derives the Service name of a port: ClusterIP ports share the
// app's own service, each NodePort/LoadBalancer port gets its own service
// because the type is a Service-level attribute (design §8).
func serviceName(app string, p PortSpec) string {
	switch p.Type {
	case PortTypeNodePort:
		return fitDNS1123(app + "-nodeport-" + strconv.Itoa(int(p.Port)))
	case PortTypeLoadBalancer:
		return fitDNS1123(app + "-lb-" + strconv.Itoa(int(p.Port)))
	}
	return app
}

// fitDNS1123 truncates a generated name to the 63-character DNS label limit
// without leaving a trailing dash.
func fitDNS1123(name string) string {
	if len(name) <= 63 {
		return name
	}
	return strings.TrimRight(name[:63], "-")
}

func buildWorkload(app string, spec AppSpec, namespace string) *unstructured.Unstructured {
	container := map[string]interface{}{
		"name":  containerName,
		"image": spec.Workload.Image,
	}
	if ports := containerPorts(spec.Ports); len(ports) > 0 {
		container["ports"] = ports
	}
	if env := envList(spec.Workload.Env); len(env) > 0 {
		container["env"] = env
	}
	if res := resourceRequirements(spec.Workload); res != nil {
		container["resources"] = res
	}
	podSpec := map[string]interface{}{"containers": []interface{}{container}}
	if vols, mounts := configVolumes(app, spec); len(vols) > 0 {
		podSpec["volumes"] = vols
		container["volumeMounts"] = mounts
	}
	specMap := map[string]interface{}{
		"selector": map[string]interface{}{
			"matchLabels": map[string]interface{}{store.LabelAppName: app},
		},
		"template": map[string]interface{}{
			"metadata": map[string]interface{}{"labels": appLabels(app)},
			"spec":     podSpec,
		},
	}
	if spec.Workload.Replicas > 0 {
		specMap["replicas"] = int64(spec.Workload.Replicas)
	}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
	obj.SetAPIVersion("apps/v1")
	obj.SetKind(workloadKind(spec.Workload.Type))
	obj.SetName(app)
	obj.SetNamespace(namespace)
	stampLabels(obj, app)
	obj.Object["spec"] = specMap
	return obj
}

// containerPorts renders the container port list, deduplicated by port
// number: several Service rows may expose the same container port.
func containerPorts(ports []PortSpec) []interface{} {
	seen := make(map[int64]bool, len(ports))
	var out []interface{}
	for _, p := range ports {
		if seen[int64(p.Port)] {
			continue
		}
		seen[int64(p.Port)] = true
		out = append(out, map[string]interface{}{
			"name":          portName(p),
			"containerPort": int64(p.Port),
			"protocol":      "TCP",
		})
	}
	return out
}

func envList(env []EnvVar) []interface{} {
	var out []interface{}
	for _, e := range env {
		if e.Name == "" || e.Value == "" {
			continue
		}
		out = append(out, map[string]interface{}{"name": e.Name, "value": e.Value})
	}
	return out
}

func resourceRequirements(w WorkloadSpec) map[string]interface{} {
	requests := quantityMap(w.Requests)
	limits := quantityMap(w.Limits)
	if requests == nil && limits == nil {
		return nil
	}
	res := map[string]interface{}{}
	if requests != nil {
		res["requests"] = requests
	}
	if limits != nil {
		res["limits"] = limits
	}
	return res
}

func quantityMap(r ResourceSpec) map[string]interface{} {
	m := map[string]interface{}{}
	if r.CPU != "" {
		m["cpu"] = r.CPU
	}
	if r.Memory != "" {
		m["memory"] = r.Memory
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// configVolumes renders the config map / secret volumes and the per-file
// subPath mounts (design §9): each file mounts at <mount point>/<file name>
// so only that single file is covered.
func configVolumes(app string, spec AppSpec) (vols, mounts []interface{}) {
	var cmFiles, secretFiles []ConfigFile
	for _, f := range spec.ConfigFiles {
		if f.MountPath == "" {
			continue
		}
		if f.Secret {
			secretFiles = append(secretFiles, f)
		} else {
			cmFiles = append(cmFiles, f)
		}
	}
	if len(cmFiles) > 0 {
		vols = append(vols, map[string]interface{}{
			"name":      configVolumeName,
			"configMap": map[string]interface{}{"name": app + "-config", "defaultMode": int64(420)},
		})
		for _, f := range cmFiles {
			mounts = append(mounts, fileMount(configVolumeName, f))
		}
	}
	if len(secretFiles) > 0 {
		vols = append(vols, map[string]interface{}{
			"name":   secretVolumeName,
			"secret": map[string]interface{}{"secretName": app + "-secret", "defaultMode": int64(420)},
		})
		for _, f := range secretFiles {
			mounts = append(mounts, fileMount(secretVolumeName, f))
		}
	}
	return vols, mounts
}

func fileMount(volume string, f ConfigFile) map[string]interface{} {
	return map[string]interface{}{
		"name":      volume,
		"mountPath": path.Join(f.MountPath, f.Name),
		"subPath":   f.Name,
	}
}

// buildServiceDocs renders the Service set from the port list (design §8):
// all ClusterIP ports merge into one service named after the app, every
// NodePort/LoadBalancer port becomes an independent service of that type.
func buildServiceDocs(app string, spec AppSpec, namespace string) []unstructured.Unstructured {
	var clusterIP []PortSpec
	var external []PortSpec
	seen := make(map[string]bool, len(spec.Ports))
	for _, p := range spec.Ports {
		if p.Type == PortTypeClusterIP {
			clusterIP = append(clusterIP, p)
			continue
		}
		key := p.Type + ":" + strconv.Itoa(int(p.Port))
		if seen[key] {
			continue
		}
		seen[key] = true
		external = append(external, p)
	}
	docs := make([]unstructured.Unstructured, 0, 1+len(external))
	if len(clusterIP) > 0 {
		docs = append(docs, buildService(app, namespace, "ClusterIP", clusterIP))
	}
	for _, p := range external {
		t := "NodePort"
		if p.Type == PortTypeLoadBalancer {
			t = "LoadBalancer"
		}
		docs = append(docs, buildService(app, namespace, t, []PortSpec{p}))
	}
	return docs
}

func buildService(app, namespace, svcType string, ports []PortSpec) unstructured.Unstructured {
	list := make([]interface{}, 0, len(ports))
	for _, p := range ports {
		list = append(list, map[string]interface{}{
			"name":       portName(p),
			"port":       int64(p.Port),
			"targetPort": int64(p.Port),
			"protocol":   "TCP",
		})
	}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
	obj.SetAPIVersion("v1")
	obj.SetKind("Service")
	obj.SetName(serviceName(app, ports[0]))
	obj.SetNamespace(namespace)
	stampLabels(obj, app)
	obj.Object["spec"] = map[string]interface{}{
		"type":     svcType,
		"selector": map[string]interface{}{store.LabelAppName: app},
		"ports":    list,
	}
	return *obj
}

// buildIngressDoc renders one Ingress document whose rules map each external
// host/path to the port's service (design §8); nil when no port declares a
// host.
func buildIngressDoc(app string, spec AppSpec, namespace string) *unstructured.Unstructured {
	byHost := make(map[string][]PortSpec)
	var hosts []string
	for _, p := range spec.Ports {
		if p.Host == "" {
			continue
		}
		if _, ok := byHost[p.Host]; !ok {
			hosts = append(hosts, p.Host)
		}
		byHost[p.Host] = append(byHost[p.Host], p)
	}
	if len(hosts) == 0 {
		return nil
	}
	rules := make([]interface{}, 0, len(hosts))
	for _, h := range hosts {
		paths := make([]interface{}, 0, len(byHost[h]))
		for _, p := range byHost[h] {
			pp := p.Path
			if pp == "" {
				pp = "/"
			}
			paths = append(paths, map[string]interface{}{
				"path":     pp,
				"pathType": ingressPathType,
				"backend": map[string]interface{}{
					"service": map[string]interface{}{
						"name": serviceName(app, p),
						"port": map[string]interface{}{"number": int64(p.Port)},
					},
				},
			})
		}
		rules = append(rules, map[string]interface{}{"host": h, "http": map[string]interface{}{"paths": paths}})
	}
	specMap := map[string]interface{}{"rules": rules}
	if spec.IngressClass != "" {
		specMap["ingressClassName"] = spec.IngressClass
	}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
	obj.SetAPIVersion("networking.k8s.io/v1")
	obj.SetKind("Ingress")
	obj.SetName(app)
	obj.SetNamespace(namespace)
	stampLabels(obj, app)
	obj.Object["spec"] = specMap
	return obj
}

// buildConfigDocs renders the app's ConfigMap (non-secret files, data) and
// Secret (sensitive files, stringData) documents (design §9).
func buildConfigDocs(app string, spec AppSpec, namespace string) []unstructured.Unstructured {
	var cm, secret map[string]interface{}
	for _, f := range spec.ConfigFiles {
		target := &cm
		if f.Secret {
			target = &secret
		}
		if *target == nil {
			*target = map[string]interface{}{}
		}
		(*target)[f.Name] = f.Content
	}
	var docs []unstructured.Unstructured
	if cm != nil {
		docs = append(docs, buildConfigDoc("ConfigMap", app+configSuffix, "data", cm, namespace, app))
	}
	if secret != nil {
		docs = append(docs, buildConfigDoc("Secret", app+secretSuffix, "stringData", secret, namespace, app))
	}
	return docs
}

func buildConfigDoc(kind, name, dataKey string, data map[string]interface{}, namespace, app string) unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{dataKey: data}}
	obj.SetAPIVersion("v1")
	obj.SetKind(kind)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	stampLabels(obj, app)
	if kind == "Secret" {
		obj.Object["type"] = "Opaque"
	}
	return *obj
}
