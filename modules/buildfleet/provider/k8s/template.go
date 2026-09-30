// Package k8s supplies buildkitd runtimes as rootless pods. Pod shape is
// declarative (BasePod + per-profile overrides) with map-merge / list-append
// / scalar-replace semantics; the image is injected from global config at
// provision time, never from the template (DESIGN.md §5.2).
package k8s

import (
	corev1 "k8s.io/api/core/v1"
)

type PodTemplate struct {
	Annotations       map[string]string
	Labels            map[string]string
	NodeSelector      map[string]string
	Tolerations       []corev1.Toleration
	Affinity          *corev1.Affinity
	Env               []corev1.EnvVar
	Volumes           []corev1.Volume
	SecurityContext   *corev1.PodSecurityContext
	ServiceAccount    string
	PriorityClassName string
}

// Merge applies override over base: maps merge key-wise (override wins),
// lists append, scalars replace when override is non-zero.
func Merge(base, over *PodTemplate) *PodTemplate {
	if base == nil {
		return over
	}
	if over == nil {
		return base
	}
	out := &PodTemplate{
		Annotations:     mergeMap(base.Annotations, over.Annotations),
		Labels:          mergeMap(base.Labels, over.Labels),
		NodeSelector:    mergeMap(base.NodeSelector, over.NodeSelector),
		Tolerations:     append(append([]corev1.Toleration{}, base.Tolerations...), over.Tolerations...),
		Affinity:        firstNonNil(base.Affinity, over.Affinity),
		Env:             append(append([]corev1.EnvVar{}, base.Env...), over.Env...),
		Volumes:         append(append([]corev1.Volume{}, base.Volumes...), over.Volumes...),
		SecurityContext: firstNonNil(base.SecurityContext, over.SecurityContext),
	}
	if over.ServiceAccount != "" {
		out.ServiceAccount = over.ServiceAccount
	} else {
		out.ServiceAccount = base.ServiceAccount
	}
	if over.PriorityClassName != "" {
		out.PriorityClassName = over.PriorityClassName
	} else {
		out.PriorityClassName = base.PriorityClassName
	}
	return out
}

func mergeMap(base, over map[string]string) map[string]string {
	if base == nil && over == nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func firstNonNil[T any](a, b *T) *T {
	if b != nil {
		return b
	}
	return a
}
