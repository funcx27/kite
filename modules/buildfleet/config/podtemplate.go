package config

import (
	"encoding/json"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	sigsyaml "sigs.k8s.io/yaml"
)

// PodTemplate is the declarative pod shape for k8s providers: managed
// cluster annotations (e.g. k8s.aliyun.com/eci-*), tolerations, affinity
// and extra env/volumes layered under buildfleet's own pod fields. Field
// names follow the k8s API (tolerationSeconds, hostPath, ...); the image
// is global config and never part of the template (DESIGN.md §5.2).
//
// The yaml tags exist for marshaling (-print-config); decoding always
// goes through UnmarshalYAML, which bridges to JSON-tag semantics.
type PodTemplate struct {
	Annotations       map[string]string          `yaml:"annotations" json:"annotations"`
	Labels            map[string]string          `yaml:"labels" json:"labels"`
	NodeSelector      map[string]string          `yaml:"nodeSelector" json:"nodeSelector"`
	Tolerations       []corev1.Toleration        `yaml:"tolerations" json:"tolerations"`
	Affinity          *corev1.Affinity           `yaml:"affinity" json:"affinity"`
	Env               []corev1.EnvVar            `yaml:"env" json:"env"`
	Volumes           []corev1.Volume            `yaml:"volumes" json:"volumes"`
	SecurityContext   *corev1.PodSecurityContext `yaml:"securityContext" json:"securityContext"`
	ServiceAccount    string                     `yaml:"serviceAccount" json:"serviceAccount"`
	PriorityClassName string                     `yaml:"priorityClassName" json:"priorityClassName"`
}

// UnmarshalYAML bridges through sigs.k8s.io/yaml (YAML -> JSON -> struct):
// yaml.v3 alone ignores the corev1 json tags and would mis-key multi-word
// fields like tolerationSeconds. Strict mode rejects unknown fields,
// matching the loader's KnownFields behavior.
func (t *PodTemplate) UnmarshalYAML(value *yaml.Node) error {
	raw, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return sigsyaml.UnmarshalStrict(raw, t)
}

// MarshalYAML mirrors UnmarshalYAML so -print-config round-trips with
// k8s API field names instead of yaml.v3's lowercased Go names.
func (t PodTemplate) MarshalYAML() (interface{}, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
