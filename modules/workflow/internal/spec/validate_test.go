package spec

import (
	"strings"
	"testing"
)

func TestValidateNodeType(t *testing.T) {
	tests := []struct {
		name        string
		typ         string
		wantTypeErr bool
	}{
		{"empty type allowed", "", false},
		{"build-image known", NodeBuildImage, false},
		{"deploy-app known", NodeDeployApp, false},
		{"approval known", NodeApproval, false},
		{"notify known", NodeNotify, false},
		{"unknown rejected", "cron-job", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := Validate(DAGSpec{Nodes: []Node{{ID: "n1", Type: tt.typ}}}, nil)
			for _, e := range errs {
				if strings.Contains(e, "unknown type") != tt.wantTypeErr {
					t.Errorf("Validate reported %q for type %q (wantTypeErr = %v)", e, tt.typ, tt.wantTypeErr)
				}
			}
		})
	}
}

func TestRegisterNodeType(t *testing.T) {
	const custom = "artifact-promote"
	dag := DAGSpec{Nodes: []Node{{ID: "n1", Type: custom}}}

	if errs := Validate(dag, nil); len(errs) == 0 {
		t.Fatalf("Validate accepted unregistered type %q", custom)
	}
	RegisterNodeType(custom)
	for _, e := range Validate(dag, nil) {
		if strings.Contains(e, "unknown type") {
			t.Errorf("Validate still rejects registered type %q: %s", custom, e)
		}
	}
}
