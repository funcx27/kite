package render

import (
	"encoding/base64"
	"fmt"
	"path"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/funcx27/kite/modules/application/internal/store"
)

// ExtractFields reads the form-covered fields out of a manifest set into the
// wizard-shaped view (design §3): the form is a partial view of the
// manifests, so fields it does not cover (strategy, affinity, extra
// containers, unknown documents, ExtraYAML) are not represented here, and
// InjectFields preserves them. Malformed fragments are skipped rather than
// failing the whole read.
func ExtractFields(app string, docs []unstructured.Unstructured) AppSpec {
	spec := AppSpec{Workload: WorkloadSpec{Type: WorkloadDeployment, Replicas: 1}}
	w := FindWorkload(app, docs)
	if w >= 0 {
		spec.Workload.Type = strings.ToLower(docs[w].GetKind())
		if r, ok, _ := unstructured.NestedInt64(docs[w].Object, "spec", "replicas"); ok && r > 0 {
			spec.Workload.Replicas = int32(r)
		}
		if c := primaryContainer(&docs[w]); c != nil {
			spec.Workload.Image, _, _ = unstructured.NestedString(c, "image")
			spec.Workload.Env = extractEnv(c)
			spec.Workload.Requests = extractQuantity(c, "requests")
			spec.Workload.Limits = extractQuantity(c, "limits")
			spec.MountPoints = extractMountPoints(c)
			spec.ConfigFiles = extractConfigFiles(app, docs, subPathMounts(c))
		}
	}
	if spec.ConfigFiles == nil {
		spec.ConfigFiles = extractConfigFiles(app, docs, nil)
	}
	spec.Ports = extractPorts(app, docs)
	spec.IngressClass = extractIngressClass(app, docs)
	return spec
}

// FindWorkload returns the index of the app's workload document: label match
// first, then name match, then the first Deployment/StatefulSet.
func FindWorkload(app string, docs []unstructured.Unstructured) int {
	for i := range docs {
		if isWorkloadKind(&docs[i]) && docs[i].GetLabels()[store.LabelKiteName] == app {
			return i
		}
	}
	for i := range docs {
		if isWorkloadKind(&docs[i]) && docs[i].GetName() == app {
			return i
		}
	}
	for i := range docs {
		if isWorkloadKind(&docs[i]) {
			return i
		}
	}
	return -1
}

func containerList(doc *unstructured.Unstructured) []interface{} {
	l, _, _ := unstructured.NestedSlice(doc.Object, "spec", "template", "spec", "containers")
	return l
}

// primaryContainer returns the form-managed container: the one named "app"
// (as rendered) or the first container.
func primaryContainer(doc *unstructured.Unstructured) map[string]interface{} {
	list := containerList(doc)
	i := PrimaryContainerIndex(list)
	if i < 0 {
		return nil
	}
	m, _ := store.AsMap(list[i])
	return m
}

// PrimaryContainerIndex returns the index of the form-managed container: the
// one named "app" (as rendered) or the first map entry.
func PrimaryContainerIndex(containers []interface{}) int {
	for i, e := range containers {
		if m, ok := store.AsMap(e); ok && m["name"] == containerName {
			return i
		}
	}
	for i, e := range containers {
		if _, ok := store.AsMap(e); ok {
			return i
		}
	}
	return -1
}

// extractEnv collects name/value pairs; valueFrom entries surface with an
// empty value and are restored verbatim by injectContainer.
func extractEnv(container map[string]interface{}) []EnvVar {
	raw, _, _ := unstructured.NestedSlice(container, "env")
	var out []EnvVar
	seen := map[string]bool{}
	for _, e := range raw {
		m, ok := store.AsMap(e)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		value, _ := m["value"].(string)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, EnvVar{Name: name, Value: value})
	}
	return out
}

func extractQuantity(container map[string]interface{}, section string) ResourceSpec {
	m, _, _ := unstructured.NestedStringMap(container, "resources", section)
	return ResourceSpec{CPU: m["cpu"], Memory: m["memory"]}
}

// extractMountPoints derives the mount point set from the module-owned
// subPath mounts: the directory part of each mount path (design §9).
func extractMountPoints(container map[string]interface{}) []string {
	set := map[string]bool{}
	for _, dir := range subPathMounts(container) {
		set[dir] = true
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// subPathMounts maps subPath → mount point directory for the module-owned
// config/secret volume mounts.
func subPathMounts(container map[string]interface{}) map[string]string {
	raw, _, _ := unstructured.NestedSlice(container, "volumeMounts")
	out := map[string]string{}
	for _, e := range raw {
		m, ok := store.AsMap(e)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		subPath, _ := m["subPath"].(string)
		mountPath, _ := m["mountPath"].(string)
		if (name == configVolumeName || name == secretVolumeName) && subPath != "" && mountPath != "" {
			out[subPath] = path.Dir(mountPath)
		}
	}
	return out
}

// extractConfigFiles reads the app config map (data) and secret
// (stringData, falling back to base64-decoded data) documents (design §9).
// Files without a matching mount get an empty MountPath.
func extractConfigFiles(app string, docs []unstructured.Unstructured, mounts map[string]string) []ConfigFile {
	var out []ConfigFile
	for i := range docs {
		d := &docs[i]
		var secret bool
		switch {
		case d.GetKind() == "ConfigMap" && d.GetName() == app+configSuffix:
		case d.GetKind() == "Secret" && d.GetName() == app+secretSuffix:
			secret = true
		default:
			continue
		}
		var values map[string]interface{}
		if secret {
			values, _, _ = unstructured.NestedMap(d.Object, "stringData")
			if len(values) == 0 {
				values = decodeSecretData(d)
			}
		} else {
			values, _, _ = unstructured.NestedMap(d.Object, "data")
		}
		for _, k := range sortedKeys(values) {
			content, _ := values[k].(string)
			out = append(out, ConfigFile{Name: k, MountPath: mounts[k], Content: content, Secret: secret})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Secret != out[j].Secret {
			return !out[i].Secret
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func sortedKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func decodeSecretData(doc *unstructured.Unstructured) map[string]interface{} {
	raw, _, _ := unstructured.NestedMap(doc.Object, "data")
	out := make(map[string]interface{}, len(raw))
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if dec, err := base64.StdEncoding.DecodeString(s); err == nil {
			out[k] = string(dec)
		} else {
			out[k] = s
		}
	}
	return out
}

// extractPorts rebuilds the port list from the app's services (design §8)
// and claims host/path from the app's ingress rules by backend reference.
func extractPorts(app string, docs []unstructured.Unstructured) []PortSpec {
	bySvc := map[string][]PortSpec{}
	var svcOrder []string
	for i := range docs {
		d := &docs[i]
		if d.GetKind() != "Service" || d.GetLabels()[store.LabelKiteName] != app {
			continue
		}
		svcType, _, _ := unstructured.NestedString(d.Object, "spec", "type")
		var pt string
		switch svcType {
		case "", "ClusterIP":
			pt = PortTypeClusterIP
		case "NodePort":
			pt = PortTypeNodePort
		case "LoadBalancer":
			pt = PortTypeLoadBalancer
		default:
			continue
		}
		ports, _, _ := unstructured.NestedSlice(d.Object, "spec", "ports")
		for _, e := range ports {
			m, ok := store.AsMap(e)
			if !ok {
				continue
			}
			num, ok, _ := unstructured.NestedInt64(m, "port")
			if !ok || num < 1 || num > 65535 {
				continue
			}
			name, _ := m["name"].(string)
			if _, seen := bySvc[d.GetName()]; !seen {
				svcOrder = append(svcOrder, d.GetName())
			}
			bySvc[d.GetName()] = append(bySvc[d.GetName()], PortSpec{Port: int32(num), Type: pt, Name: name})
		}
	}
	claimIngressHosts(app, docs, bySvc)
	var out []PortSpec
	seen := map[string]bool{}
	for _, svc := range svcOrder {
		for _, p := range bySvc[svc] {
			key := p.Type + ":" + fmt.Sprint(p.Port)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, p)
		}
	}
	return out
}

// claimIngressHosts assigns host/path to the port specs referenced by the
// app's ingress backends; later rules for already-claimed ports are ignored.
func claimIngressHosts(app string, docs []unstructured.Unstructured, bySvc map[string][]PortSpec) {
	for i := range docs {
		d := &docs[i]
		if d.GetKind() != "Ingress" || d.GetLabels()[store.LabelKiteName] != app {
			continue
		}
		rules, _, _ := unstructured.NestedSlice(d.Object, "spec", "rules")
		for _, r := range rules {
			rm, ok := store.AsMap(r)
			if !ok {
				continue
			}
			host, _ := rm["host"].(string)
			paths, _, _ := unstructured.NestedSlice(rm, "http", "paths")
			for _, p := range paths {
				pm, ok := store.AsMap(p)
				if !ok || host == "" {
					continue
				}
				svcName, _, _ := unstructured.NestedString(pm, "backend", "service", "name")
				candidates := bySvc[svcName]
				num, hasNum, _ := unstructured.NestedInt64(pm, "backend", "service", "port", "number")
				portName, hasName, _ := unstructured.NestedString(pm, "backend", "service", "port", "name")
				for j := range candidates {
					if candidates[j].Host != "" {
						continue
					}
					if hasNum && candidates[j].Port != int32(num) {
						continue
					}
					if hasName && candidates[j].Name != portName {
						continue
					}
					candidates[j].Host = host
					candidates[j].Path = "/"
					if pth, _ := pm["path"].(string); pth != "" {
						candidates[j].Path = pth
					}
					break
				}
			}
		}
	}
}

func extractIngressClass(app string, docs []unstructured.Unstructured) string {
	for i := range docs {
		d := &docs[i]
		if d.GetKind() != "Ingress" || d.GetLabels()[store.LabelKiteName] != app {
			continue
		}
		if class, ok, _ := unstructured.NestedString(d.Object, "spec", "ingressClassName"); ok {
			return class
		}
	}
	return ""
}

// InjectFields applies the form-covered fields onto a copy of the manifest
// set (design §3/§7 PUT {spec}): form-derived documents (the workload's
// container fields, services, ingress, the app config map/secret) are
// rebuilt, everything else — unknown fields, foreign documents, extra
// containers — passes through verbatim.
func InjectFields(app, namespace string, docs []unstructured.Unstructured, spec AppSpec) ([]unstructured.Unstructured, error) {
	if !ValidateDNS1123Label(app, 63) {
		return nil, fmt.Errorf("application name %q must be a DNS-1123 label of at most 63 characters", app)
	}
	spec.Normalize(app)
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("invalid spec: %w", err)
	}
	out := make([]unstructured.Unstructured, 0, len(docs)+4)
	workloadSeen := false
	for i := range docs {
		d := &docs[i]
		switch {
		case isWorkloadKind(d) && ownedByApp(d, app):
			out = append(out, *injectWorkload(app, d, spec))
			workloadSeen = true
		case (d.GetKind() == "Service" || d.GetKind() == "Ingress") && d.GetLabels()[store.LabelKiteName] == app:
			// Rebuilt from spec.Ports below (design §8: derived documents).
		case (d.GetKind() == "ConfigMap" && d.GetName() == app+configSuffix) ||
			(d.GetKind() == "Secret" && d.GetName() == app+secretSuffix):
			// Rebuilt from spec.ConfigFiles below (design §9).
		default:
			out = append(out, *d.DeepCopy())
		}
	}
	if !workloadSeen {
		out = append(out, *buildWorkload(app, spec, namespace))
	}
	out = append(out, buildServiceDocs(app, spec, namespace)...)
	if ing := buildIngressDoc(app, spec, namespace); ing != nil {
		out = append(out, *ing)
	}
	out = append(out, buildConfigDocs(app, spec, namespace)...)
	extra, err := store.ParseExtraYAML(spec.ExtraYAML)
	if err != nil {
		return nil, fmt.Errorf("parse extraYaml: %w", err)
	}
	for i := range extra {
		stampLabels(&extra[i], app)
		out = upsertDoc(out, extra[i])
	}
	return out, nil
}

// ownedByApp matches the module identity label first and the workload name
// second, so YAML-mode edits that keep either identity still map to the form.
func ownedByApp(d *unstructured.Unstructured, app string) bool {
	return d.GetLabels()[store.LabelKiteName] == app || d.GetName() == app
}

// injectWorkload returns a deep copy of doc with the form fields applied.
func injectWorkload(app string, doc *unstructured.Unstructured, spec AppSpec) *unstructured.Unstructured {
	out := doc.DeepCopy()
	out.SetAPIVersion("apps/v1")
	out.SetKind(workloadKind(spec.Workload.Type))
	stampLabels(out, app)
	specMap, ok := store.AsMap(out.Object["spec"])
	if !ok {
		specMap = map[string]interface{}{}
		out.Object["spec"] = specMap
	}
	if spec.Workload.Replicas > 0 {
		specMap["replicas"] = int64(spec.Workload.Replicas)
	} else {
		delete(specMap, "replicas")
	}
	tpl, ok := store.AsMap(specMap["template"])
	if !ok {
		tpl = map[string]interface{}{}
		specMap["template"] = tpl
	}
	meta, ok := store.AsMap(tpl["metadata"])
	if !ok {
		meta = map[string]interface{}{}
		tpl["metadata"] = meta
	}
	labels, _ := store.AsMap(meta["labels"])
	if labels == nil {
		labels = map[string]interface{}{}
		meta["labels"] = labels
	}
	for k, v := range appLabels(app) {
		labels[k] = v
	}
	podSpec, ok := store.AsMap(tpl["spec"])
	if !ok {
		podSpec = map[string]interface{}{}
		tpl["spec"] = podSpec
	}
	containers, _ := store.AsList(podSpec["containers"])
	idx := PrimaryContainerIndex(containers)
	if idx < 0 {
		containers = append(containers, map[string]interface{}{"name": containerName})
		idx = len(containers) - 1
		podSpec["containers"] = containers
	}
	c, _ := store.AsMap(containers[idx])
	injectContainer(app, c, podSpec, spec)
	return out
}

// injectContainer rewrites the form-owned container fields on the container
// map in place: image, ports, env, resources and the config volume mounts
// (the volumes themselves live on the pod spec).
func injectContainer(app string, c, podSpec map[string]interface{}, spec AppSpec) {
	c["image"] = spec.Workload.Image
	if ports := containerPorts(spec.Ports); len(ports) > 0 {
		c["ports"] = ports
	} else {
		delete(c, "ports")
	}
	originals := envByName(c)
	var env []interface{}
	for _, e := range spec.Workload.Env {
		if e.Value != "" {
			env = append(env, map[string]interface{}{"name": e.Name, "value": e.Value})
			continue
		}
		// Empty form value: keep the original entry (e.g. valueFrom) as-is.
		if orig, ok := originals[e.Name]; ok {
			env = append(env, orig)
		}
	}
	if len(env) > 0 {
		c["env"] = env
	} else {
		delete(c, "env")
	}
	setQuantities(c, "requests", spec.Workload.Requests)
	setQuantities(c, "limits", spec.Workload.Limits)
	injectVolumes(app, c, podSpec, spec)
}

func envByName(c map[string]interface{}) map[string]interface{} {
	raw, _, _ := unstructured.NestedSlice(c, "env")
	out := map[string]interface{}{}
	for _, e := range raw {
		m, ok := store.AsMap(e)
		if !ok {
			continue
		}
		if n, _ := m["name"].(string); n != "" {
			out[n] = m
		}
	}
	return out
}

// setQuantities writes the form-owned cpu/memory keys of one resources
// section, removing empty keys and empty sections; other quantity keys are
// preserved.
func setQuantities(c map[string]interface{}, section string, v ResourceSpec) {
	res, ok := store.AsMap(c["resources"])
	if !ok {
		if v.CPU == "" && v.Memory == "" {
			return
		}
		res = map[string]interface{}{}
		c["resources"] = res
	}
	sec, ok := store.AsMap(res[section])
	if v.CPU == "" && v.Memory == "" {
		if ok {
			delete(sec, "cpu")
			delete(sec, "memory")
			if len(sec) == 0 {
				delete(res, section)
			}
		}
		return
	}
	if !ok {
		sec = map[string]interface{}{}
		res[section] = sec
	}
	if v.CPU != "" {
		sec["cpu"] = v.CPU
	} else {
		delete(sec, "cpu")
	}
	if v.Memory != "" {
		sec["memory"] = v.Memory
	} else {
		delete(sec, "memory")
	}
}

// injectVolumes rebuilds the module-owned config/secret volumes and their
// per-file mounts, leaving foreign volumes and mounts untouched (design §9).
// Volumes live on the pod spec, mounts on the container; a stray
// container-level volumes list (written by the pre-fix inject path) is
// invalid and the API server drops it, so it is removed to keep the stored
// manifests convergent with the cluster.
func injectVolumes(app string, c, podSpec map[string]interface{}, spec AppSpec) {
	delete(c, "volumes")
	vols, _ := store.AsList(podSpec["volumes"])
	mounts, _ := store.AsList(c["volumeMounts"])
	var newVols, newMounts []interface{}
	for _, v := range vols {
		if m, ok := store.AsMap(v); ok && ownedVolumeName(m) {
			continue
		}
		newVols = append(newVols, v)
	}
	for _, m := range mounts {
		if v, ok := store.AsMap(m); ok && ownedVolumeName(v) {
			continue
		}
		newMounts = append(newMounts, m)
	}
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
		newVols = append(newVols, map[string]interface{}{
			"name":      configVolumeName,
			"configMap": map[string]interface{}{"name": app + configSuffix, "defaultMode": int64(420)},
		})
		for _, f := range cmFiles {
			newMounts = append(newMounts, fileMount(configVolumeName, f))
		}
	}
	if len(secretFiles) > 0 {
		newVols = append(newVols, map[string]interface{}{
			"name":   secretVolumeName,
			"secret": map[string]interface{}{"secretName": app + secretSuffix, "defaultMode": int64(420)},
		})
		for _, f := range secretFiles {
			newMounts = append(newMounts, fileMount(secretVolumeName, f))
		}
	}
	if len(newVols) > 0 {
		podSpec["volumes"] = newVols
	} else {
		delete(podSpec, "volumes")
	}
	if len(newMounts) > 0 {
		c["volumeMounts"] = newMounts
	} else {
		delete(c, "volumeMounts")
	}
}

func ownedVolumeName(m map[string]interface{}) bool {
	n, _ := m["name"].(string)
	return n == configVolumeName || n == secretVolumeName
}

// upsertDoc replaces the document with the same (kind, namespace, name) or
// appends it, keeping ExtraYAML injections idempotent.
func upsertDoc(docs []unstructured.Unstructured, doc unstructured.Unstructured) []unstructured.Unstructured {
	key := store.DocKeyOf(&doc)
	for i := range docs {
		if store.DocKeyOf(&docs[i]) == key {
			docs[i] = doc
			return docs
		}
	}
	return append(docs, doc)
}
