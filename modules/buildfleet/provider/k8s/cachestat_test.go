package k8s

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/funcx27/kite/modules/buildfleet/runtime"
)

// fakeRegistry serves two projects sharing one base layer.
func TestStatRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/_catalog":
			fmt.Fprint(w, `{"repositories":["buildfleet-cache/team/demo","buildfleet-cache/other-app","unrelated/repo"]}`)
		case r.URL.Path == "/v2/buildfleet-cache/team/demo/tags/list":
			fmt.Fprint(w, `{"tags":["latest"]}`)
		case r.URL.Path == "/v2/buildfleet-cache/other-app/tags/list":
			fmt.Fprint(w, `{"tags":["latest"]}`)
		case r.URL.Path == "/v2/buildfleet-cache/team/demo/manifests/latest":
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			fmt.Fprint(w, `{"config":{"digest":"sha256:c1"},"layers":[{"digest":"sha256:shared"},{"digest":"sha256:d1"}]}`)
		case r.URL.Path == "/v2/buildfleet-cache/other-app/manifests/latest":
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			fmt.Fprint(w, `{"config":{"digest":"sha256:c1"},"layers":[{"digest":"sha256:shared"}]}`)
		default:
			// blob HEAD: distinct sizes per digest
			sizes := map[string]int64{"sha256:shared": 100, "sha256:d1": 50, "sha256:c1": 10}
			for d, n := range sizes {
				if r.URL.Path == "/v2/buildfleet-cache/team/demo/blobs/"+d && r.Method == http.MethodHead {
					w.Header().Set("Content-Length", fmt.Sprint(n))
					return
				}
				if r.URL.Path == "/v2/buildfleet-cache/other-app/blobs/"+d && r.Method == http.MethodHead {
					w.Header().Set("Content-Length", fmt.Sprint(n))
					return
				}
			}
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	snap, err := statRegistry(t.Context(), srv.URL, "buildfleet-cache")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]runtime.ProjectStat{}
	for _, p := range snap.Projects {
		byName[p.Project] = p
	}
	if len(snap.Projects) != 2 {
		t.Fatalf("projects: %+v", snap.Projects)
	}
	demo := byName["team/demo"]
	other := byName["other-app"]
	// demo: shared(100) + d1(50) exclusive, config c1(10) shared (both reference it)
	if demo.Exclusive != 50 || demo.Shared != 110 {
		t.Fatalf("demo attribution: excl=%d shared=%d", demo.Exclusive, demo.Shared)
	}
	if other.Exclusive != 0 || other.Shared != 110 {
		t.Fatalf("other attribution: excl=%d shared=%d", other.Exclusive, other.Shared)
	}
	// total = dedup: shared(100)+d1(50)+c1(10)
	if snap.TotalBytes != 160 {
		t.Fatalf("total = %d, want 160", snap.TotalBytes)
	}
}
