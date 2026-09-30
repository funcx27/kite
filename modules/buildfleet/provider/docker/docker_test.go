package docker

import (
	"testing"

	"github.com/funcx27/kite/modules/buildfleet/resource"
)

func TestCapacityAccounting(t *testing.T) {
	total := resource.Resources{CPU: 8, Memory: 16 * resource.Giga}
	p, err := New(Config{Total: total, Image: "moby/buildkit:rootless"})
	if err != nil {
		t.Fatal(err)
	}

	if got := p.Capacity(); !got.Fits(resource.Resources{CPU: 8}) {
		t.Fatalf("fresh provider should have full capacity, got %v", got)
	}
	p.trackLive(resource.Resources{CPU: 6, Memory: 12 * resource.Giga})
	if got := p.Capacity(); got.Fits(resource.Resources{CPU: 3}) {
		t.Fatalf("over-committed capacity reported: %v", got)
	}
	p.untrackLive(resource.Resources{CPU: 6, Memory: 12 * resource.Giga})
	if got := p.Capacity(); !got.Fits(resource.Resources{CPU: 8}) {
		t.Fatalf("dispose did not return capacity, got %v", got)
	}
}

func TestEndpointClass(t *testing.T) {
	for _, tt := range []struct {
		address  string
		hostIP   string
		addrHost string
		wantErr  bool
	}{
		{address: "", hostIP: "127.0.0.1", addrHost: "127.0.0.1"},
		{address: "unix:///var/run/docker.sock", hostIP: "127.0.0.1", addrHost: "127.0.0.1"},
		{address: "npipe:////./pipe/docker_engine", hostIP: "127.0.0.1", addrHost: "127.0.0.1"},
		{address: "tcp://192.168.1.10:2375", hostIP: "", addrHost: "192.168.1.10"},
		{address: "tcp://docker.internal:2375", hostIP: "", addrHost: "docker.internal"},
		{address: "ftp://x.example.com", wantErr: true},
		{address: "https://x.example.com", wantErr: true}, // docker SDK accepts tcp/unix/npipe only
		{address: "tcp://", wantErr: true},
	} {
		hostIP, addrHost, err := endpointClass(tt.address)
		if tt.wantErr {
			if err == nil {
				t.Errorf("endpointClass(%q): want error, got %q/%q", tt.address, hostIP, addrHost)
			}
			continue
		}
		if err != nil || hostIP != tt.hostIP || addrHost != tt.addrHost {
			t.Errorf("endpointClass(%q) = %q/%q, %v; want %q/%q", tt.address, hostIP, addrHost, err, tt.hostIP, tt.addrHost)
		}
	}
}

func TestRemoteDaemonRejectsTomlBindMount(t *testing.T) {
	if _, err := New(Config{
		Address:  "tcp://192.168.1.10:2375",
		TomlPath: "/tmp/buildfleet-store/buildkitd.toml",
	}); err == nil {
		t.Fatal("remote daemon + local TomlPath bind mount must fail")
	}
	// Remote without a toml (capacity-only accounting) stays constructible.
	if _, err := New(Config{Address: "tcp://192.168.1.10:2375"}); err != nil {
		t.Fatalf("remote daemon without toml should construct: %v", err)
	}
}

func TestAddrJoinsEndpointHost(t *testing.T) {
	p, err := New(Config{Address: "tcp://192.168.1.10:2375", Total: resource.Resources{CPU: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.addr("34567"); got != "tcp://192.168.1.10:34567" {
		t.Fatalf("addr = %q", got)
	}
	local, err := New(Config{Total: resource.Resources{CPU: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got := local.addr("34567"); got != "tcp://127.0.0.1:34567" {
		t.Fatalf("local addr = %q", got)
	}
}
