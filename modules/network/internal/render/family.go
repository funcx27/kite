package render

import "strings"

// Controller families the module tailors rendering for; the empty string
// behaves like FamilyOther (family-neutral rendering).
const (
	FamilyNginx   = "nginx"
	FamilyTraefik = "traefik"
	FamilyKong    = "kong"
	FamilyALB     = "alb"
	FamilyOther   = "other"
)

// ControllerFamily maps an IngressClass controller to the controller family
// the module tailors rendering and annotation validation for; it is also the
// decision point where future target families (e.g. Gateway API) plug in.
func ControllerFamily(controller string) string {
	switch {
	case strings.HasPrefix(controller, "k8s.io/ingress-nginx"),
		strings.HasPrefix(controller, "kubernetes.io/ingress-nginx"):
		return FamilyNginx
	case strings.Contains(controller, "traefik"):
		return FamilyTraefik
	case strings.HasPrefix(controller, "konghq.com/"):
		return FamilyKong
	case strings.HasPrefix(controller, "ingress.k8s.alibabacloud/"):
		return FamilyALB
	default:
		return FamilyOther
	}
}
