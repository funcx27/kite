// Package render derives the Kubernetes objects a network host row should
// converge to and compares them against live cluster state. Everything here
// is pure: no IO, no cluster access.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/funcx27/kite/modules/network/internal/store"
)

// Label and annotation keys the module stamps on every object it owns; the
// apply path relies on them for the ownership check.
const (
	LabelManaged = "kite.network/managed"
	LabelHost    = "kite.network/host"
	// LabelSpace mirrors the application module's LabelKiteSpace value; the
	// module cannot import the host side, so it is fixed here.
	LabelSpace            = "kite.application/space"
	AnnotationHost        = "kite.network/host"
	AnnotationCertificate = "kite.network/certificate"
	AnnotationCertHash    = "kite.network/cert-hash"
)

// Options carries the target-family inputs of the desired-object derivation.
// ControllerFamily selects family-specific behavior (e.g. the ALB certificate
// auto-match TLS mode) and is the extension point for future target kinds
// such as Gateway API; the package stays pure — no IO.
type Options struct {
	ControllerFamily string
}

// DesiredIngresses derives every object the host row should converge to: the
// main object holding the routes without service-level annotations (the
// historically shared name, unchanged behavior), plus one object per service
// whose routes carry annotations, grouped by appName regardless of port.
func DesiredIngresses(h *store.Host, opts Options) ([]*networkingv1.Ingress, error) {
	routes, catalog, err := decodeHost(h)
	if err != nil {
		return nil, err
	}
	host := normalizeHost(h.Host)
	var plain []store.Route
	grouped := map[string][]store.Route{}
	var apps []string
	for _, r := range routes {
		if len(r.Annotations) == 0 {
			plain = append(plain, r)
			continue
		}
		if _, seen := grouped[r.AppName]; !seen {
			apps = append(apps, r.AppName)
		}
		grouped[r.AppName] = append(grouped[r.AppName], r)
	}
	slices.Sort(apps)

	main := buildIngress(h, host, ResourceName(host), catalog, ingressRules(plain, host), opts)
	out := []*networkingv1.Ingress{main}
	for _, app := range apps {
		name := ServiceIngressName(host, app)
		out = append(out, buildIngress(h, host, name, serviceAnnotations(catalog, grouped[app]),
			ingressRules(grouped[app], host), opts))
	}
	return out, nil
}

// decodeHost deserializes the row's stored routes and annotation catalog.
func decodeHost(h *store.Host) ([]store.Route, map[string]string, error) {
	var routes []store.Route
	if err := h.Routes.Unmarshal(&routes); err != nil {
		return nil, nil, fmt.Errorf("unmarshal routes for %s: %w", h.Host, err)
	}
	catalog := map[string]string{}
	if err := h.Annotations.Unmarshal(&catalog); err != nil {
		return nil, nil, fmt.Errorf("unmarshal annotations for %s: %w", h.Host, err)
	}
	return routes, catalog, nil
}

// buildIngress assembles one desired object: management labels, the host
// markers over the given annotation base, the class, rules and TLS block.
func buildIngress(h *store.Host, host, name string, annotations map[string]string, rules []networkingv1.IngressRule, opts Options) *networkingv1.Ingress {
	className := h.IngressClass
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   h.Namespace,
			Labels:      managementLabels(host, h.Namespace),
			Annotations: desiredAnnotations(annotations, host, h.TLSCertificate),
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &className,
			Rules:            rules,
		},
	}
	ing.Spec.TLS = desiredTLS(host, name, h.TLSCertificate, opts.ControllerFamily)
	return ing
}

// desiredTLS: with a certificate the block binds the managed secret; without
// one on the ALB family it lists the host only (the controller matches
// account certificates itself); otherwise there is no TLS block.
func desiredTLS(host, name, certificate, family string) []networkingv1.IngressTLS {
	switch {
	case certificate != "":
		return []networkingv1.IngressTLS{{Hosts: []string{host}, SecretName: name}}
	case family == FamilyALB:
		return []networkingv1.IngressTLS{{Hosts: []string{host}}}
	default:
		return nil
	}
}

// serviceAnnotations merges the group's service-level annotations over the
// host-level catalog; service keys win, later routes win on conflict (the
// write path normalizes one appName to a single set, so this only matters
// for rows stored before that rule).
func serviceAnnotations(catalog map[string]string, routes []store.Route) map[string]string {
	merged := make(map[string]string, len(catalog))
	for k, v := range catalog {
		merged[k] = v
	}
	for _, r := range routes {
		for k, v := range r.Annotations {
			merged[k] = v
		}
	}
	return merged
}

// DesiredTLSSecret builds the kubernetes.io/tls Secret carrying the
// certificate data; it shares the derived name with the Ingress.
func DesiredTLSSecret(h *store.Host, crt, key []byte) *corev1.Secret {
	host := normalizeHost(h.Host)
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        ResourceName(host),
			Namespace:   h.Namespace,
			Labels:      managementLabels(host, h.Namespace),
			Annotations: map[string]string{AnnotationCertHash: CertHash(crt, key)},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       crt,
			corev1.TLSPrivateKeyKey: key,
		},
	}
}

// CertHash fingerprints a key pair so apply can skip the Secret write when
// the live content is unchanged. PEM never contains NUL, so the separator
// keeps the two fields unambiguous.
func CertHash(crt, key []byte) string {
	buf := make([]byte, 0, len(crt)+1+len(key))
	buf = append(buf, crt...)
	buf = append(buf, 0)
	buf = append(buf, key...)
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

func managementLabels(host, space string) map[string]string {
	return map[string]string{
		LabelManaged: "true",
		LabelHost:    host,
		LabelSpace:   space,
	}
}

func desiredAnnotations(catalog map[string]string, host, certificate string) map[string]string {
	ann := make(map[string]string, len(catalog)+2)
	for k, v := range catalog {
		ann[k] = v
	}
	ann[AnnotationHost] = host
	if certificate != "" {
		ann[AnnotationCertificate] = certificate
	}
	return ann
}

func ingressRules(routes []store.Route, host string) []networkingv1.IngressRule {
	if len(routes) == 0 {
		return nil
	}
	pathType := networkingv1.PathTypePrefix
	paths := make([]networkingv1.HTTPIngressPath, 0, len(routes))
	for _, r := range routes {
		paths = append(paths, networkingv1.HTTPIngressPath{
			Path:     r.Path,
			PathType: &pathType,
			Backend: networkingv1.IngressBackend{
				Service: &networkingv1.IngressServiceBackend{
					Name: r.AppName,
					Port: networkingv1.ServiceBackendPort{Number: r.Port},
				},
			},
		})
	}
	// The rule carries the domain so the rendered object is a complete,
	// standard Ingress (kubectl-applicable and re-importable; the host is
	// still pinned by name, labels and TLS).
	return []networkingv1.IngressRule{{
		Host: host,
		IngressRuleValue: networkingv1.IngressRuleValue{
			HTTP: &networkingv1.HTTPIngressRuleValue{Paths: paths},
		},
	}}
}
