package render

import (
	"testing"

	networkingv1 "k8s.io/api/networking/v1"

	"github.com/funcx27/kite/modules/network/internal/store"
)

func desiredIngress(t *testing.T, cert string) *networkingv1.Ingress {
	t.Helper()
	h := hostFixture(t, func(h *store.Host) { h.TLSCertificate = cert })
	out, err := DesiredIngresses(h, Options{})
	if err != nil {
		t.Fatalf("DesiredIngresses: %v", err)
	}
	return out[0]
}

func TestCompare(t *testing.T) {
	tests := []struct {
		name   string
		cert   string
		mutate func(live *networkingv1.Ingress)
		want   bool
	}{
		{"identical renders equal", "", func(*networkingv1.Ingress) {}, true},
		{"rule order shuffled", "", func(l *networkingv1.Ingress) {
			paths := l.Spec.Rules[0].HTTP.Paths
			paths[0], paths[1] = paths[1], paths[0]
		}, true},
		{"external annotation added", "", func(l *networkingv1.Ingress) {
			l.Annotations["cert-manager.io/issuer"] = "letsencrypt"
		}, true},
		{"rule removed", "", func(l *networkingv1.Ingress) {
			p := l.Spec.Rules[0].HTTP.Paths
			l.Spec.Rules[0].HTTP.Paths = p[:1]
		}, false},
		{"path changed", "", func(l *networkingv1.Ingress) {
			l.Spec.Rules[0].HTTP.Paths[0].Path = "/other"
		}, false},
		{"pathType changed", "", func(l *networkingv1.Ingress) {
			*l.Spec.Rules[0].HTTP.Paths[0].PathType = networkingv1.PathTypeImplementationSpecific
		}, false},
		{"backend service changed", "", func(l *networkingv1.Ingress) {
			l.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name = "other"
		}, false},
		{"backend port changed", "", func(l *networkingv1.Ingress) {
			l.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number = 9090
		}, false},
		{"ingress class changed", "", func(l *networkingv1.Ingress) {
			*l.Spec.IngressClassName = "traefik"
		}, false},
		{"ingress class cleared", "", func(l *networkingv1.Ingress) {
			l.Spec.IngressClassName = nil
		}, false},
		{"platform annotation value changed", "", func(l *networkingv1.Ingress) {
			l.Annotations["nginx.ingress.kubernetes.io/proxy-body-size"] = "16m"
		}, false},
		{"platform annotation removed", "", func(l *networkingv1.Ingress) {
			delete(l.Annotations, "nginx.ingress.kubernetes.io/proxy-body-size")
		}, false},
		{"tls equal with certificate", "wildcard-example-com", func(*networkingv1.Ingress) {}, true},
		{"desired tls, live dropped it", "wildcard-example-com", func(l *networkingv1.Ingress) {
			l.Spec.TLS = nil
		}, false},
		{"tls secret name changed", "wildcard-example-com", func(l *networkingv1.Ingress) {
			l.Spec.TLS[0].SecretName = "other-secret"
		}, false},
		{"desired no tls, live gained one", "", func(l *networkingv1.Ingress) {
			l.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{"api.example.com"}, SecretName: l.Name}}
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired := desiredIngress(t, tt.cert)
			live := desired.DeepCopy()
			tt.mutate(live)
			if got := Compare(desired, live); got != tt.want {
				t.Errorf("Compare = %v, want %v", got, tt.want)
			}
		})
	}
}

// A missing live object is drift by definition.
func TestCompareNilLive(t *testing.T) {
	if Compare(desiredIngress(t, ""), nil) {
		t.Error("Compare(desired, nil) = true, want false")
	}
}

func TestLoadBalancerAddress(t *testing.T) {
	lb := func(entries ...networkingv1.IngressLoadBalancerIngress) *networkingv1.Ingress {
		return &networkingv1.Ingress{Status: networkingv1.IngressStatus{
			LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: entries},
		}}
	}
	entry := func(ip, hostname string) networkingv1.IngressLoadBalancerIngress {
		return networkingv1.IngressLoadBalancerIngress{IP: ip, Hostname: hostname}
	}
	tests := []struct {
		name string
		ing  *networkingv1.Ingress
		want string
	}{
		{"first non-empty ip wins", lb(entry("", ""), entry("203.0.113.10", "lb.example.com"), entry("198.51.100.7", "")), "203.0.113.10"},
		{"hostname fallback when no ip", lb(entry("", "lb.example.com"), entry("", "other.example.com")), "lb.example.com"},
		{"all entries empty", lb(entry("", ""), entry("", "")), ""},
		{"no status entries", lb(), ""},
		{"nil ingress", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LoadBalancerAddress(tt.ing); got != tt.want {
				t.Errorf("LoadBalancerAddress = %q, want %q", got, tt.want)
			}
		})
	}
}
