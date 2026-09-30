package config

import (
	"strings"
	"testing"
	"time"
)

func TestECIValidate(t *testing.T) {
	cases := []struct {
		name    string
		eci     ECI
		wantErr string
	}{
		{name: "empty"},
		{name: "image cache only", eci: ECI{ImageCache: true}},
		{name: "market spot", eci: ECI{SpotStrategy: "SpotAsPriceGo"}},
		{
			name: "limited spot",
			eci:  ECI{SpotStrategy: "SpotWithPriceLimit", SpotPriceLimit: "0.25"},
		},
		{
			name: "limited spot pair",
			eci:  ECI{SpotStrategy: "SpotWithPriceLimit", SpotPriceLimit: "0.25,0.35"},
		},
		{
			name:    "unknown strategy",
			eci:     ECI{SpotStrategy: "Spot"},
			wantErr: "spotStrategy",
		},
		{
			name:    "limit without strategy",
			eci:     ECI{SpotPriceLimit: "0.25"},
			wantErr: "requires spotStrategy",
		},
		{
			name:    "strategy without limit",
			eci:     ECI{SpotStrategy: "SpotWithPriceLimit"},
			wantErr: "requires spotPriceLimit",
		},
		{
			name: "limit with market strategy",
			eci:  ECI{SpotStrategy: "SpotAsPriceGo", SpotPriceLimit: "0.25"},
			// SpotWithPriceLimit already covers the pair rule above; this
			// guards the opposite pairing.
			wantErr: "requires spotStrategy",
		},
		{
			name:    "too many prices",
			eci:     ECI{SpotStrategy: "SpotWithPriceLimit", SpotPriceLimit: "0.1,0.2,0.3"},
			wantErr: "vCPU[,perGiB]",
		},
		{
			name:    "non-numeric price",
			eci:     ECI{SpotStrategy: "SpotWithPriceLimit", SpotPriceLimit: "cheap"},
			wantErr: "positive numbers",
		},
		{
			name:    "negative price",
			eci:     ECI{SpotStrategy: "SpotWithPriceLimit", SpotPriceLimit: "-1"},
			wantErr: "positive numbers",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.eci.validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestECITemplate(t *testing.T) {
	eci := &ECI{
		ImageCache:     true,
		SpotStrategy:   "SpotWithPriceLimit",
		SpotPriceLimit: "0.25, 0.35", // spaces normalized out of the annotation
		InstanceTypes:  []string{"ecs.c8i.xlarge", "ecs.c8i.2xlarge"},
	}
	ann := eci.Template().Annotations
	want := map[string]string{
		"k8s.aliyun.com/eci-image-cache":      "true",
		"k8s.aliyun.com/eci-spot-strategy":    "SpotWithPriceLimit",
		"k8s.aliyun.com/eci-spot-price-limit": "0.25,0.35",
		"k8s.aliyun.com/eci-instance-type":    "ecs.c8i.xlarge,ecs.c8i.2xlarge",
	}
	if len(ann) != len(want) {
		t.Fatalf("annotations = %v", ann)
	}
	for k, v := range want {
		if ann[k] != v {
			t.Fatalf("annotation %s = %q, want %q", k, ann[k], v)
		}
	}
}

func TestECITemplateEmpty(t *testing.T) {
	ann := (&ECI{}).Template().Annotations
	if len(ann) != 0 {
		t.Fatalf("empty ECI must translate to no annotations, got %v", ann)
	}
}

func TestLoadECIK8sOnly(t *testing.T) {
	_, err := Load(writeConfig(t, "providers:\n  - type: docker\n    eci: {imageCache: true}\n"))
	if err == nil || !strings.Contains(err.Error(), "k8s-only") {
		t.Fatalf("docker eci must fail, got %v", err)
	}
	_, err = Load(writeConfig(t, "providers:\n  - type: docker\n    podTemplate: {serviceAccount: x}\n"))
	if err == nil || !strings.Contains(err.Error(), "k8s-only") {
		t.Fatalf("docker podTemplate must fail, got %v", err)
	}
	// Validation errors surface with the field path.
	_, err = Load(writeConfig(t, "providers:\n  - type: k8s\n    eci: {spotPriceLimit: \"0.25\"}\n"))
	if err == nil || !strings.Contains(err.Error(), "providers[0].eci") {
		t.Fatalf("eci validation must fail with path, got %v", err)
	}
}

func TestLoadStartupCost(t *testing.T) {
	c, err := Load(writeConfig(t, "providers:\n  - type: k8s\n    startupCost: 15s\n"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.Providers[0].StartupCostParsed()
	if err != nil || d != 15*time.Second {
		t.Fatalf("startupCost = %v err=%v", d, err)
	}
	_, err = Load(writeConfig(t, "providers:\n  - type: k8s\n    startupCost: fast\n"))
	if err == nil || !strings.Contains(err.Error(), "startupCost") {
		t.Fatalf("bad startupCost must fail, got %v", err)
	}
}
