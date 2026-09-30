package registry

import "testing"

func TestRenderToml(t *testing.T) {
	cfgs := []Config{
		{Host: "self-signed.example.com", Insecure: true},
		{Host: "registry.local:5000", HTTP: true},
		{Host: "mirror.example.com", Insecure: true},
		{Host: "docker.io", Mirrors: []string{"mirror.example.com/dockerhub"}},
	}
	got, err := RenderToml(cfgs)
	if err != nil {
		t.Fatal(err)
	}
	want := `[registry."self-signed.example.com"]
  insecure = true

[registry."registry.local:5000"]
  http = true

[registry."mirror.example.com"]
  insecure = true

[registry."docker.io"]
  mirrors = ["mirror.example.com/dockerhub"]
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
