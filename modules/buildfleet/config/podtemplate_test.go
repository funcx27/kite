package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
)

// The critical bridge property: k8s API field names with embedded capitals
// (tolerationSeconds) decode to the right fields, and unknown fields fail.
func TestLoadPodTemplate(t *testing.T) {
	c, err := Load(writeConfig(t, `
providers:
  - type: k8s
    podTemplate:
      annotations: {k8s.aliyun.com/eci-image-cache: "true"}
      labels: {team: build}
      nodeSelector: {type: eci}
      serviceAccount: buildfleet-sa
      priorityClassName: build-critical
      tolerations:
        - key: spot
          operator: Exists
          tolerationSeconds: 3600
      env:
        - name: BUILDKITD_FLAGS
          value: --debug
`))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := c.Providers[0].PodTemplate
	if tmpl == nil {
		t.Fatal("podTemplate lost")
	}
	if tmpl.Annotations["k8s.aliyun.com/eci-image-cache"] != "true" {
		t.Fatalf("annotations: %v", tmpl.Annotations)
	}
	if tmpl.Labels["team"] != "build" || tmpl.NodeSelector["type"] != "eci" {
		t.Fatalf("labels/nodeSelector: %v %v", tmpl.Labels, tmpl.NodeSelector)
	}
	if tmpl.ServiceAccount != "buildfleet-sa" || tmpl.PriorityClassName != "build-critical" {
		t.Fatalf("scalars: %q %q", tmpl.ServiceAccount, tmpl.PriorityClassName)
	}
	if len(tmpl.Tolerations) != 1 {
		t.Fatalf("tolerations: %+v", tmpl.Tolerations)
	}
	tol := tmpl.Tolerations[0]
	if tol.Key != "spot" || tol.Operator != corev1.TolerationOpExists {
		t.Fatalf("toleration fields: %+v", tol)
	}
	if tol.TolerationSeconds == nil || *tol.TolerationSeconds != 3600 {
		t.Fatalf("tolerationSeconds (json-tag bridge): %+v", tol.TolerationSeconds)
	}
	if len(tmpl.Env) != 1 || tmpl.Env[0].Name != "BUILDKITD_FLAGS" || tmpl.Env[0].Value != "--debug" {
		t.Fatalf("env: %+v", tmpl.Env)
	}
}

func TestLoadPodTemplateStrict(t *testing.T) {
	_, err := Load(writeConfig(t, `
providers:
  - type: k8s
    podTemplate:
      tolerations:
        - key: spot
          tolerationSecond: 3600
`))
	if err == nil || !strings.Contains(err.Error(), "tolerationSecond") {
		t.Fatalf("typo'd k8s field must fail loudly, got %v", err)
	}
	_, err = Load(writeConfig(t, "providers:\n  - type: k8s\n    podTemplate: {unknownField: x}\n"))
	if err == nil || !strings.Contains(err.Error(), "unknownField") {
		t.Fatalf("unknown podTemplate key must fail loudly, got %v", err)
	}
}

// -print-config marshals with k8s API field names, not yaml.v3's
// lowercased Go names.
func TestPodTemplateMarshal(t *testing.T) {
	sec := int64(3600)
	tmpl := PodTemplate{
		Tolerations: []corev1.Toleration{{Key: "spot", TolerationSeconds: &sec}},
	}
	b, err := yaml.Marshal(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "tolerationSeconds: 3600") {
		t.Fatalf("marshal must use k8s field names, got:\n%s", s)
	}
	if strings.Contains(s, "tolerationseconds") {
		t.Fatalf("lowercased field leaked into marshal:\n%s", s)
	}
}
