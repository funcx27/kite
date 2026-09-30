package k8s

import (
	"os"
	"testing"
)

// TestProvisionE2E exercises ConfigMap+Pod+Service creation and teardown
// against a real cluster. Set BUILDFLEET_K8S_E2E (namespace) to enable.
func TestProvisionE2E(t *testing.T) {
	if os.Getenv("BUILDFLEET_K8S_E2E") == "" {
		t.Skip("needs cluster")
	}
	t.Skip("TODO: wire clientset from in-cluster/kubeconfig when a cluster is available")
}
