package k8s

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	bfresource "github.com/funcx27/kite/modules/buildfleet/resource"
	"github.com/funcx27/kite/modules/buildfleet/runtime"
)

func TestMergeSemantics(t *testing.T) {
	base := &PodTemplate{
		Annotations: map[string]string{"eci": "1", "team": "base"},
		Labels:      map[string]string{"app": "buildkit"},
		Env:         []corev1.EnvVar{{Name: "A", Value: "1"}},
		Tolerations: []corev1.Toleration{{Key: "spot"}},
	}
	over := &PodTemplate{
		Annotations: map[string]string{"team": "custom"},
		Env:         []corev1.EnvVar{{Name: "B", Value: "2"}},
	}

	got := Merge(base, over)
	if got.Annotations["team"] != "custom" {
		t.Fatal("map merge: override must win")
	}
	if got.Annotations["eci"] != "1" {
		t.Fatal("map merge: non-overridden keys must survive")
	}
	if len(got.Env) != 2 {
		t.Fatalf("list merge: append semantics, got %d", len(got.Env))
	}
	if len(got.Tolerations) != 1 {
		t.Fatal("list merge: untouched lists must survive")
	}
	if got.Labels["app"] != "buildkit" {
		t.Fatal("labels must survive")
	}
}

func TestMergeScalarsAndPointers(t *testing.T) {
	base := &PodTemplate{ServiceAccount: "base-sa", PriorityClassName: "base-pc"}
	over := &PodTemplate{ServiceAccount: "over-sa"}

	got := Merge(base, over)
	if got.ServiceAccount != "over-sa" {
		t.Fatalf("scalar override must win, got %q", got.ServiceAccount)
	}
	if got.PriorityClassName != "base-pc" {
		t.Fatalf("zero override must keep base, got %q", got.PriorityClassName)
	}

	aff := &corev1.Affinity{}
	got = Merge(&PodTemplate{}, &PodTemplate{Affinity: aff})
	if got.Affinity != aff {
		t.Fatal("pointer: firstNonNil must take override")
	}
	got = Merge(&PodTemplate{Affinity: aff}, nil)
	if got.Affinity != aff {
		t.Fatal("nil override must keep base pointer")
	}
}

func TestMergeNil(t *testing.T) {
	if Merge(nil, nil) != nil {
		t.Fatal("both nil must return nil")
	}
	base := &PodTemplate{ServiceAccount: "x"}
	if got := Merge(base, nil); got != base {
		t.Fatal("nil override must return base")
	}
	over := &PodTemplate{ServiceAccount: "y"}
	if got := Merge(nil, over); got != over {
		t.Fatal("nil base must return override")
	}
}

func TestBuildPodPrivileged(t *testing.T) {
	base := &Provider{cfg: Config{Privileged: true}}
	pod := base.buildPod("bf-x", runtime.Spec{Resources: bfresource.Resources{CPU: 1, Memory: bfresource.Giga}}, &PodTemplate{})
	sc := pod.Spec.Containers[0].SecurityContext
	if sc == nil || sc.Privileged == nil || !*sc.Privileged {
		t.Fatalf("privileged flag not applied: %+v", sc)
	}

	noPriv := &Provider{cfg: Config{}}
	pod2 := noPriv.buildPod("bf-x", runtime.Spec{Resources: bfresource.Resources{CPU: 1, Memory: bfresource.Giga}}, &PodTemplate{})
	if pod2.Spec.Containers[0].SecurityContext != nil {
		t.Fatal("unprivileged provider must not set container SecurityContext")
	}
}

func TestBuildPodRootless(t *testing.T) {
	p := &Provider{cfg: Config{Rootless: true}}
	pod := p.buildPod("bf-x", runtime.Spec{Resources: bfresource.Resources{CPU: 1, Memory: bfresource.Giga}}, &PodTemplate{})
	c := pod.Spec.Containers[0]
	found := false
	for _, arg := range c.Command {
		if arg == "--oci-worker-no-process-sandbox" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rootless command flag missing: %v", c.Command)
	}
	sc := c.SecurityContext
	if sc == nil || sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeUnconfined {
		t.Fatalf("rootless seccomp profile: %+v", sc)
	}
	if sc.Privileged != nil {
		t.Fatal("rootless must not be privileged")
	}
}

func TestBuildPodEphemeralStorage(t *testing.T) {
	p := New(Config{Namespace: "default"})
	pod := p.buildPod("x", runtime.Spec{Resources: bfresource.Resources{
		CPU: 2, Memory: 4 * bfresource.Giga, EphemeralStorage: 20 * bfresource.Giga,
	}}, &PodTemplate{})
	lim := pod.Spec.Containers[0].Resources.Limits
	if got := lim[corev1.ResourceEphemeralStorage]; got.String() != "20Gi" {
		t.Fatalf("ephemeral limit = %v", got.String())
	}

	pod = p.buildPod("x", runtime.Spec{Resources: bfresource.Resources{CPU: 2, Memory: 4 * bfresource.Giga}}, &PodTemplate{})
	if _, ok := pod.Spec.Containers[0].Resources.Limits[corev1.ResourceEphemeralStorage]; ok {
		t.Fatal("zero ephemeral storage must not set the limit key")
	}
}

// The BasePod template (podTemplate + cloud-block translations) layers
// under buildfleet's own labels/annotations without clobbering them.
func TestBuildPodBaseTemplate(t *testing.T) {
	p := New(Config{Namespace: "ns"})
	pod := p.buildPod("bf-x", runtime.Spec{Resources: bfresource.Resources{CPU: 1, Memory: bfresource.Giga}}, &PodTemplate{
		Annotations:    map[string]string{"k8s.aliyun.com/eci-image-cache": "true"},
		Labels:         map[string]string{"team": "build"},
		NodeSelector:   map[string]string{"type": "eci"},
		Tolerations:    []corev1.Toleration{{Key: "spot", Operator: corev1.TolerationOpExists}},
		ServiceAccount: "buildfleet-sa",
	})
	if pod.Annotations["k8s.aliyun.com/eci-image-cache"] != "true" {
		t.Fatalf("template annotations lost: %v", pod.Annotations)
	}
	if pod.Annotations["buildfleet/config-gen"] != "" {
		t.Fatal("template annotations must not drop buildfleet's own")
	}
	if pod.Labels["buildfleet"] != "runtime" || pod.Labels["buildfleet-id"] != "bf-x" || pod.Labels["team"] != "build" {
		t.Fatalf("labels: %v", pod.Labels)
	}
	if pod.Spec.NodeSelector["type"] != "eci" {
		t.Fatalf("nodeSelector: %v", pod.Spec.NodeSelector)
	}
	if len(pod.Spec.Tolerations) != 1 || pod.Spec.Tolerations[0].Key != "spot" {
		t.Fatalf("tolerations: %+v", pod.Spec.Tolerations)
	}
	if pod.Spec.ServiceAccountName != "buildfleet-sa" {
		t.Fatalf("serviceAccount: %q", pod.Spec.ServiceAccountName)
	}
}

func TestWithStartupCost(t *testing.T) {
	p := &Provider{cfg: Config{StartupCost: 15 * time.Second}}
	if got := p.withStartupCost(runtime.Spec{}); got.StartupCost != 15*time.Second {
		t.Fatalf("raise to provider cost, got %v", got.StartupCost)
	}
	if got := p.withStartupCost(runtime.Spec{StartupCost: 30 * time.Second}); got.StartupCost != 30*time.Second {
		t.Fatalf("caller-declared cost must not lower, got %v", got.StartupCost)
	}
	cheap := &Provider{cfg: Config{}}
	if got := cheap.withStartupCost(runtime.Spec{StartupCost: time.Second}); got.StartupCost != time.Second {
		t.Fatalf("unset provider cost must not touch spec, got %v", got.StartupCost)
	}
}
