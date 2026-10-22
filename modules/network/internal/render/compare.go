package render

import (
	"fmt"
	"slices"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
)

// Compare reports whether live matches desired on the fields the platform
// owns: ingress class, the flattened path set (order-insensitive), the TLS
// blocks, and the values of the annotations the platform renders.
// Annotations live carries beyond the desired set are treated as externally
// added and do not count as drift.
func Compare(desired, live *networkingv1.Ingress) bool {
	if live == nil {
		return false
	}
	if !classNameEqual(desired.Spec.IngressClassName, live.Spec.IngressClassName) {
		return false
	}
	if !slices.Equal(pathKeys(desired.Spec.Rules), pathKeys(live.Spec.Rules)) {
		return false
	}
	if !slices.Equal(tlsKeys(desired.Spec.TLS), tlsKeys(live.Spec.TLS)) {
		return false
	}
	for k, v := range desired.Annotations {
		if live.Annotations[k] != v {
			return false
		}
	}
	return true
}

// LoadBalancerAddress returns the first non-empty IP across the status
// entries, or the first non-empty hostname when no entry carries an IP.
func LoadBalancerAddress(ing *networkingv1.Ingress) string {
	if ing == nil {
		return ""
	}
	entries := ing.Status.LoadBalancer.Ingress
	for _, e := range entries {
		if e.IP != "" {
			return e.IP
		}
	}
	for _, e := range entries {
		if e.Hostname != "" {
			return e.Hostname
		}
	}
	return ""
}

func classNameEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// pathKeys flattens every rule path into a canonical string so rule sets
// compare order-insensitively. NUL separates fields; it cannot occur in the
// legal values being joined.
func pathKeys(rules []networkingv1.IngressRule) []string {
	var keys []string
	for _, rule := range rules {
		if rule.HTTP == nil {
			continue
		}
		for _, p := range rule.HTTP.Paths {
			var service string
			var port int32
			if p.Backend.Service != nil {
				service = p.Backend.Service.Name
				port = p.Backend.Service.Port.Number
			}
			pathType := ""
			if p.PathType != nil {
				pathType = string(*p.PathType)
			}
			keys = append(keys, fmt.Sprintf("%s\x00%s\x00%s\x00%d", p.Path, pathType, service, port))
		}
	}
	slices.Sort(keys)
	return keys
}

// tlsKeys canonicalizes each TLS block (hosts sorted, plus secretName) so
// entry order and host order do not matter.
func tlsKeys(tls []networkingv1.IngressTLS) []string {
	keys := make([]string, 0, len(tls))
	for _, t := range tls {
		hosts := slices.Clone(t.Hosts)
		slices.Sort(hosts)
		keys = append(keys, strings.Join(hosts, "\x00")+"\x00"+t.SecretName)
	}
	slices.Sort(keys)
	return keys
}
