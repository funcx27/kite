package render

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/funcx27/kite/modules/network/internal/store"
)

func hostFixture(t *testing.T, mutate func(*store.Host)) *store.Host {
	t.Helper()
	h := &store.Host{
		Host:         "Api.Example.COM",
		Cluster:      "prod",
		Namespace:    "team-alpha",
		IngressClass: "nginx",
	}
	if err := h.Routes.Marshal([]store.Route{
		{Path: "/api", AppName: "api-gateway", Port: 8080},
		{Path: "/", AppName: "web", Port: 80},
	}); err != nil {
		t.Fatalf("marshal routes: %v", err)
	}
	if err := h.Annotations.Marshal(map[string]string{
		"nginx.ingress.kubernetes.io/proxy-body-size": "8m",
	}); err != nil {
		t.Fatalf("marshal annotations: %v", err)
	}
	if mutate != nil {
		mutate(h)
	}
	return h
}

func TestDesiredIngress(t *testing.T) {
	tests := []struct {
		name          string
		family        string
		cert          string
		wantTLSLen    int
		wantTLSSecret string // expected SecretName of the first TLS block
	}{
		{"with certificate", "", "wildcard-example-com", 1, ResourceName("api.example.com")},
		{"without certificate", "", "", 0, ""},
		{"alb without certificate auto-matches", FamilyALB, "", 1, ""},
		{"alb with certificate keeps secret", FamilyALB, "wildcard-example-com", 1, ResourceName("api.example.com")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := hostFixture(t, func(h *store.Host) {
				h.TLSCertificate = tt.cert
			})
			ing, err := soleIngress(t, h, Options{ControllerFamily: tt.family})
			if err != nil {
				t.Fatalf("DesiredIngresses: %v", err)
			}

			wantName := ResourceName("api.example.com")
			if ing.Name != wantName || ing.Namespace != "team-alpha" {
				t.Errorf("meta = %s/%s, want %s/team-alpha", ing.Namespace, ing.Name, wantName)
			}
			wantLabels := map[string]string{
				LabelManaged: "true",
				LabelHost:    "api.example.com",
				LabelSpace:   "team-alpha",
			}
			if !reflect.DeepEqual(ing.Labels, wantLabels) {
				t.Errorf("labels = %v, want %v", ing.Labels, wantLabels)
			}
			wantAnn := map[string]string{
				AnnotationHost: "api.example.com",
				"nginx.ingress.kubernetes.io/proxy-body-size": "8m",
			}
			if tt.wantTLSLen == 1 && tt.cert != "" {
				wantAnn[AnnotationCertificate] = tt.cert
			}
			if !reflect.DeepEqual(ing.Annotations, wantAnn) {
				t.Errorf("annotations = %v, want %v", ing.Annotations, wantAnn)
			}
			if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "nginx" {
				t.Errorf("ingressClassName = %v, want nginx", ing.Spec.IngressClassName)
			}

			if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].HTTP == nil || ing.Spec.Rules[0].Host != "api.example.com" {
				t.Fatalf("rules = %+v, want one HTTP rule on the domain host", ing.Spec.Rules)
			}
			type flatPath struct {
				path, pathType, service string
				port                    int32
			}
			var got []flatPath
			for _, p := range ing.Spec.Rules[0].HTTP.Paths {
				if p.PathType == nil || p.Backend.Service == nil {
					t.Fatalf("path %+v must set pathType and service backend", p)
				}
				got = append(got, flatPath{p.Path, string(*p.PathType), p.Backend.Service.Name, p.Backend.Service.Port.Number})
			}
			want := []flatPath{
				{"/api", "Prefix", "api-gateway", 8080},
				{"/", "Prefix", "web", 80},
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("paths = %+v, want %+v", got, want)
			}

			if len(ing.Spec.TLS) != tt.wantTLSLen {
				t.Fatalf("tls = %+v, want %d entries", ing.Spec.TLS, tt.wantTLSLen)
			}
			if tt.wantTLSLen == 1 {
				tls := ing.Spec.TLS[0]
				if tls.SecretName != tt.wantTLSSecret || !reflect.DeepEqual(tls.Hosts, []string{"api.example.com"}) {
					t.Errorf("tls = %+v, want secret %q for host api.example.com", tls, tt.wantTLSSecret)
				}
			}
		})
	}
}

// soleIngress renders the single-object case and guards the expectation.
func soleIngress(t *testing.T, h *store.Host, opts Options) (*networkingv1.Ingress, error) {
	t.Helper()
	out, err := DesiredIngresses(h, opts)
	if err != nil {
		return nil, err
	}
	if len(out) != 1 {
		t.Fatalf("objects = %d, want 1 for routes without annotations", len(out))
	}
	return out[0], nil
}

// Routes carrying service-level annotations split off into one object per
// appName, ordered by name, with the service annotations layered over the
// host-level catalog; unannotated routes stay on the main object.
func TestDesiredIngressesServiceGrouping(t *testing.T) {
	h := hostFixture(t, nil)
	if err := h.Routes.Marshal([]store.Route{
		{Path: "/api", AppName: "api-gateway", Port: 8080,
			Annotations: map[string]string{"nginx.ingress.kubernetes.io/proxy-body-size": "16m"}},
		{Path: "/", AppName: "web", Port: 80},
		{Path: "/v2", AppName: "api-gateway", Port: 9090,
			Annotations: map[string]string{"nginx.ingress.kubernetes.io/proxy-body-size": "16m"}},
	}); err != nil {
		t.Fatalf("marshal routes: %v", err)
	}
	out, err := DesiredIngresses(h, Options{})
	if err != nil {
		t.Fatalf("DesiredIngresses: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("objects = %d, want 2", len(out))
	}
	main := out[0]
	wantMainPaths := []string{"/"}
	if got := livePathSet(t, main); !reflect.DeepEqual(got, wantMainPaths) {
		t.Errorf("main paths = %v, want %v", got, wantMainPaths)
	}
	extra := out[1]
	wantName := ServiceIngressName("api.example.com", "api-gateway")
	if extra.Name != wantName || len(wantName) > 63 {
		t.Errorf("service object name = %q, want %q within 63 chars", extra.Name, wantName)
	}
	wantPaths := []string{"/api", "/v2"}
	if got := livePathSet(t, extra); !reflect.DeepEqual(got, wantPaths) {
		t.Errorf("service paths = %v, want %v", got, wantPaths)
	}
	wantAnn := map[string]string{
		AnnotationHost: "api.example.com",
		"nginx.ingress.kubernetes.io/proxy-body-size": "16m",
	}
	if !reflect.DeepEqual(extra.Annotations, wantAnn) {
		t.Errorf("service annotations = %v, want %v", extra.Annotations, wantAnn)
	}
	wantLabels := main.Labels
	if !reflect.DeepEqual(extra.Labels, wantLabels) {
		t.Errorf("service labels = %v, want main labels %v", extra.Labels, wantLabels)
	}
	if !reflect.DeepEqual(extra.Spec.IngressClassName, main.Spec.IngressClassName) {
		t.Errorf("service ingressClassName = %v, want %v", extra.Spec.IngressClassName, main.Spec.IngressClassName)
	}
}

func livePathSet(t *testing.T, ing *networkingv1.Ingress) []string {
	t.Helper()
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].HTTP == nil {
		t.Fatalf("rules = %+v, want one HTTP rule", ing.Spec.Rules)
	}
	var paths []string
	for _, p := range ing.Spec.Rules[0].HTTP.Paths {
		paths = append(paths, p.Path)
	}
	return paths
}

func TestDesiredIngressZeroRow(t *testing.T) {
	h := &store.Host{Host: "api.example.com", Namespace: "team-alpha", IngressClass: "nginx"}
	out, err := DesiredIngresses(h, Options{})
	if err != nil {
		t.Fatalf("DesiredIngresses: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("objects = %d, want the main object only", len(out))
	}
	ing := out[0]
	if len(ing.Spec.Rules) != 0 {
		t.Errorf("rules = %+v, want none for empty route table", ing.Spec.Rules)
	}
	if ing.Annotations[AnnotationHost] != "api.example.com" {
		t.Errorf("annotations = %v, want %s always present", ing.Annotations, AnnotationHost)
	}
}

func TestDesiredIngressCorruptJSON(t *testing.T) {
	h := &store.Host{Host: "api.example.com", Namespace: "team-alpha", IngressClass: "nginx", Routes: store.JSONField("{bad")}
	if _, err := DesiredIngresses(h, Options{}); err == nil {
		t.Fatal("want error for corrupt routes JSON")
	}
}

func TestDesiredTLSSecret(t *testing.T) {
	h := hostFixture(t, func(h *store.Host) { h.TLSCertificate = "wildcard-example-com" })
	crt := []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n")
	key := []byte("-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----\n")
	sec := DesiredTLSSecret(h, crt, key)

	wantName := ResourceName("api.example.com")
	if sec.Name != wantName || sec.Namespace != "team-alpha" {
		t.Errorf("meta = %s/%s, want %s/team-alpha", sec.Namespace, sec.Name, wantName)
	}
	if sec.Type != corev1.SecretTypeTLS {
		t.Errorf("type = %q, want %q", sec.Type, corev1.SecretTypeTLS)
	}
	wantLabels := map[string]string{
		LabelManaged: "true",
		LabelHost:    "api.example.com",
		LabelSpace:   "team-alpha",
	}
	if !reflect.DeepEqual(sec.Labels, wantLabels) {
		t.Errorf("labels = %v, want %v", sec.Labels, wantLabels)
	}
	if string(sec.Data[corev1.TLSCertKey]) != string(crt) || string(sec.Data[corev1.TLSPrivateKeyKey]) != string(key) {
		t.Error("data must carry the key pair verbatim")
	}
	gotHash := sec.Annotations[AnnotationCertHash]
	if gotHash == "" || gotHash != CertHash(crt, key) {
		t.Errorf("cert-hash = %q, want %q", gotHash, CertHash(crt, key))
	}
}

func TestCertHash(t *testing.T) {
	tests := []struct {
		name       string
		crtA, keyA []byte
		crtB, keyB []byte
		wantSame   bool
	}{
		{"identical input", []byte("a"), []byte("b"), []byte("a"), []byte("b"), true},
		{"key material differs", []byte("a"), []byte("b"), []byte("a"), []byte("c"), false},
		{"separator keeps fields unambiguous", []byte("a"), []byte("bc"), []byte("ab"), []byte("c"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			same := CertHash(tt.crtA, tt.keyA) == CertHash(tt.crtB, tt.keyB)
			if same != tt.wantSame {
				t.Errorf("hash equality = %v, want %v", same, tt.wantSame)
			}
		})
	}
}
