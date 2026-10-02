package render

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Workload and service-port type values of the wizard spec (design §3/§8).
const (
	WorkloadDeployment  = "deployment"
	WorkloadStatefulSet = "statefulset"

	PortTypeClusterIP    = "clusterip"
	PortTypeNodePort     = "nodeport"
	PortTypeLoadBalancer = "loadbalancer"
)

// AppSpec is the structured wizard product stored as App.Spec (design §3).
// It is the draft/birth snapshot of an app; deployed configs are full
// manifests, and the form view is read back into this shape by ExtractFields.
type AppSpec struct {
	Workload     WorkloadSpec `json:"workload"`
	MountPoints  []string     `json:"mountPoints,omitempty"`
	ConfigFiles  []ConfigFile `json:"configFiles,omitempty"`
	Ports        []PortSpec   `json:"ports,omitempty"`
	IngressClass string       `json:"ingressClass,omitempty"`
	ExtraYAML    string       `json:"extraYaml,omitempty"`
}

// WorkloadSpec carries the "运行配置" wizard fields (design §3).
type WorkloadSpec struct {
	Type     string       `json:"type"`
	Image    string       `json:"image"`
	Replicas int32        `json:"replicas,omitempty"`
	Env      []EnvVar     `json:"env,omitempty"`
	Requests ResourceSpec `json:"requests,omitempty"`
	Limits   ResourceSpec `json:"limits,omitempty"`
}

// EnvVar is one plain-value environment variable; valueFrom entries surface
// here with an empty Value and are preserved verbatim by InjectFields.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// ResourceSpec is a requests/limits pair of quantity strings.
type ResourceSpec struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// PortSpec is one row of the network wizard step (design §8): a port, its
// Service type and an optional external host/path that also renders an
// Ingress rule.
type PortSpec struct {
	Port int32  `json:"port"`
	Type string `json:"type"` // clusterip (default) / nodeport / loadbalancer
	Host string `json:"host,omitempty"`
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"` // empty → auto tcp-<port>
}

// ConfigFile is one file of the config wizard step (design §9): rendered into
// the app ConfigMap (data) or Secret (stringData) and mounted subPath-wise.
type ConfigFile struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"` // must hit MountPoints; empty = unmounted
	Content   string `json:"content"`
	Secret    bool   `json:"secret"`
}

// configKeyNamePattern matches valid ConfigMap data keys / subPath names.
var configKeyNamePattern = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

// Normalize applies the documented wizard defaults so Validate and Render see
// canonical values: empty types, cleaned mount points and the /etc/<app>
// default mount point when files exist without any (design §8/§9). Requests
// left empty get the platform's overcommit profile — CPU at half the limit
// (compressible), memory at the full limit (incompressible, so overcommitting
// it risks OOM eviction) — matching the frontend editor's defaults.
func (s *AppSpec) Normalize(app string) {
	if s.Workload.Type == "" {
		s.Workload.Type = WorkloadDeployment
	}
	s.Workload.Type = strings.ToLower(s.Workload.Type)
	// Memory is canonicalized to Mi/Gi so stored specs and rendered pods
	// never carry M/G/Ki variants.
	s.Workload.Limits.Memory = canonicalMemoryQuantity(s.Workload.Limits.Memory)
	s.Workload.Requests.Memory = canonicalMemoryQuantity(s.Workload.Requests.Memory)
	if s.Workload.Limits.CPU != "" && s.Workload.Requests.CPU == "" {
		s.Workload.Requests.CPU = halfCPUQuantity(s.Workload.Limits.CPU)
	}
	if s.Workload.Limits.Memory != "" && s.Workload.Requests.Memory == "" {
		s.Workload.Requests.Memory = s.Workload.Limits.Memory
	}
	for i := range s.Ports {
		if s.Ports[i].Type == "" {
			s.Ports[i].Type = PortTypeClusterIP
		}
		s.Ports[i].Type = strings.ToLower(s.Ports[i].Type)
	}
	var points []string
	seen := make(map[string]bool, len(s.MountPoints))
	for _, p := range s.MountPoints {
		p = path.Clean(p)
		if !seen[p] {
			seen[p] = true
			points = append(points, p)
		}
	}
	if len(points) == 0 && len(s.ConfigFiles) > 0 {
		points = []string{"/etc/" + app}
	}
	s.MountPoints = points
}

// Validate checks the spec constraints Render relies on; call Normalize first.
func (s AppSpec) Validate() error {
	switch s.Workload.Type {
	case WorkloadDeployment, WorkloadStatefulSet:
	default:
		return fmt.Errorf("workload type must be %q or %q", WorkloadDeployment, WorkloadStatefulSet)
	}
	if strings.TrimSpace(s.Workload.Image) == "" {
		return fmt.Errorf("image is required")
	}
	// Limits are mandatory so a rendered pod is never BestEffort or
	// unbounded; requests default to the limits in Normalize.
	for name, v := range map[string]string{
		"cpu":    s.Workload.Limits.CPU,
		"memory": s.Workload.Limits.Memory,
	} {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("workload %s limit is required", name)
		}
		if _, err := resource.ParseQuantity(v); err != nil {
			return fmt.Errorf("workload %s limit %q is not a valid quantity", name, v)
		}
	}
	if s.Workload.Replicas < 0 {
		return fmt.Errorf("replicas must not be negative")
	}
	envNames := make(map[string]bool, len(s.Workload.Env))
	for _, e := range s.Workload.Env {
		if e.Name == "" {
			return fmt.Errorf("env var name must not be empty")
		}
		if envNames[e.Name] {
			return fmt.Errorf("duplicate env var %q", e.Name)
		}
		envNames[e.Name] = true
	}
	if err := s.validatePorts(); err != nil {
		return err
	}
	points := make(map[string]bool, len(s.MountPoints))
	for _, m := range s.MountPoints {
		if !strings.HasPrefix(m, "/") {
			return fmt.Errorf("mount point %q must be an absolute path", m)
		}
		points[m] = true
	}
	files := make(map[string]bool, len(s.ConfigFiles))
	for _, f := range s.ConfigFiles {
		if f.Name == "." || f.Name == ".." || !configKeyNamePattern.MatchString(f.Name) {
			return fmt.Errorf("config file name %q must consist of letters, digits, dashes, dots and underscores", f.Name)
		}
		key := strconv.FormatBool(f.Secret) + "/" + f.Name
		if files[key] {
			return fmt.Errorf("duplicate config file %q", f.Name)
		}
		files[key] = true
		if f.MountPath != "" && !points[f.MountPath] {
			return fmt.Errorf("config file %q: mount path %q is not in the mount point set", f.Name, f.MountPath)
		}
	}
	return nil
}

// validatePorts checks the declared ports: range, type vocabulary, optional
// name and duplication rules, host/path shape.
func (s AppSpec) validatePorts() error {
	portNames := make(map[string]bool, len(s.Ports))
	portKeys := make(map[string]bool, len(s.Ports))
	for _, p := range s.Ports {
		if p.Port < 1 || p.Port > 65535 {
			return fmt.Errorf("port %d out of range", p.Port)
		}
		switch p.Type {
		case PortTypeClusterIP, PortTypeNodePort, PortTypeLoadBalancer:
		default:
			return fmt.Errorf("port type %q must be clusterip, nodeport or loadbalancer", p.Type)
		}
		if p.Name != "" {
			if len(p.Name) > 15 || !dns1123Pattern.MatchString(p.Name) {
				return fmt.Errorf("port name %q must be a DNS-1123 label of at most 15 characters", p.Name)
			}
			if portNames[p.Name] {
				return fmt.Errorf("duplicate port name %q", p.Name)
			}
			portNames[p.Name] = true
		}
		key := p.Type + ":" + strconv.Itoa(int(p.Port))
		if portKeys[key] {
			return fmt.Errorf("duplicate %s port %d", p.Type, p.Port)
		}
		portKeys[key] = true
		if p.Host != "" {
			if strings.ContainsAny(p.Host, " /") {
				return fmt.Errorf("invalid host %q", p.Host)
			}
			if p.Path != "" && !strings.HasPrefix(p.Path, "/") {
				return fmt.Errorf("path %q must start with /", p.Path)
			}
		}
	}
	return nil
}

// halfCPUQuantity halves a CPU quantity string, flooring at the input when
// the half would round to zero; unparseable input passes through (Validate
// rejects it later).
func halfCPUQuantity(v string) string {
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return v
	}
	half := q.MilliValue() / 2
	if half == 0 {
		return v
	}
	return resource.NewMilliQuantity(half, resource.DecimalSI).String()
}

// canonicalMemoryQuantity rewrites a memory quantity in binary Mi/Gi units,
// rounded to the nearest Mi (at least 1Mi), so "512M" becomes "489Mi" and
// "1G" becomes "954Mi"; unparseable input passes through for Validate.
func canonicalMemoryQuantity(v string) string {
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return v
	}
	const mi = int64(1) << 20
	const gi = int64(1) << 30
	bytes := q.Value()
	if bytes <= 0 {
		return v
	}
	rounded := (bytes + mi/2) / mi * mi
	if rounded < mi {
		rounded = mi
	}
	if rounded >= gi && rounded%gi == 0 {
		return fmt.Sprintf("%dGi", rounded/gi)
	}
	return fmt.Sprintf("%dMi", rounded/mi)
}
