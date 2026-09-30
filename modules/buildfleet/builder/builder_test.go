package builder

import (
	"context"
	"testing"

	"github.com/moby/buildkit/client"

	"github.com/funcx27/kite/modules/buildfleet/creds"
)

func TestSessionAuths(t *testing.T) {
	t.Setenv("BF_AUTH", "user:pass")
	tbl := creds.Table{
		{Kind: creds.Registry, Match: "reg.example.com", Auth: creds.AuthRef{Env: "BF_AUTH"}},
	}
	if !AllowedHosts(tbl)("reg.example.com") {
		t.Fatal("configured host must be allowed")
	}
	if AllowedHosts(tbl)("other.example.com") {
		t.Fatal("unconfigured host must not leak creds")
	}
	auth, err := FindAuth(tbl, "reg.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if auth.Username != "user" || auth.Password != "pass" {
		t.Fatalf("got %+v", auth)
	}
}

func TestSourceSecretStore(t *testing.T) {
	store := sourceSecretStore(
		map[string]SourceSecret{
			"git.example.com:3030": {Header: "Basic Z2l0"},
		},
		map[string]SourceSecret{
			"files.example.com:8443": {Header: "Basic aHR0cA=="},
			"tokens.example.com":     {Token: "tok"},
		},
	)
	want := map[string]string{
		// git names keep the port (buildkitd matches by u.Host)
		"GIT_AUTH_HEADER.git.example.com:3030": "Basic Z2l0",
		// http names strip it (u.Hostname()) and use underscores
		"HTTP_AUTH_HEADER_files.example.com": "Basic aHR0cA==",
		"HTTP_AUTH_TOKEN_tokens.example.com": "tok",
	}
	if len(store) != len(want) {
		t.Fatalf("store: %v", store)
	}
	for name, v := range want {
		if string(store[name]) != v {
			t.Fatalf("%s: %q, want %q", name, store[name], v)
		}
	}
}

func TestProgressEvents(t *testing.T) {
	ch := make(chan *client.SolveStatus, 2)
	ch <- &client.SolveStatus{
		Vertexes: []*client.Vertex{{Name: "dockerfile"}},
		Logs:     []*client.VertexLog{{Data: []byte("step 1")}},
	}
	close(ch)

	var gotVertex, gotLog int
	err := Watch(ch, func(ev Event) bool {
		if ev.Vertex != nil {
			gotVertex++
		}
		if ev.Log != nil {
			gotLog++
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotVertex != 1 || gotLog != 1 {
		t.Fatalf("want 1 vertex + 1 log, got %d/%d", gotVertex, gotLog)
	}
}

func TestSolveOptCacheRef(t *testing.T) {
	in := SolveInput{ContextDir: t.TempDir(), Dockerfile: "Dockerfile", CacheRef: "cache.local:5000/bf"}
	opts, err := (&Builder{}).solveOpt(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.CacheImports) != 1 || opts.CacheImports[0].Type != "registry" ||
		opts.CacheImports[0].Attrs["ref"] != in.CacheRef {
		t.Fatalf("cache imports: %+v", opts.CacheImports)
	}
	if len(opts.CacheExports) != 1 || opts.CacheExports[0].Attrs["mode"] != "max" {
		t.Fatalf("cache exports: %+v", opts.CacheExports)
	}

	plain, err := (&Builder{}).solveOpt(context.Background(), SolveInput{ContextDir: t.TempDir(), Dockerfile: "Dockerfile"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.CacheImports != nil || plain.CacheExports != nil {
		t.Fatal("empty CacheRef must not set cache options")
	}
}

func TestSolveOptRemoteContext(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   SolveInput
		want string
	}{
		{
			name: "git url",
			in:   SolveInput{GitURL: "https://git.example.com/team/app.git#main"},
			want: "https://git.example.com/team/app.git#main",
		},
		{
			name: "http url",
			in:   SolveInput{HTTPURL: "https://files.example.com/team/app-1.0.tar.gz"},
			want: "https://files.example.com/team/app-1.0.tar.gz",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := (&Builder{}).solveOpt(context.Background(), tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got := opts.FrontendAttrs["context"]; got != tt.want {
				t.Fatalf("context attr: %q, want %q", got, tt.want)
			}
			if len(opts.LocalMounts) != 0 {
				t.Fatalf("remote context must not set local mounts: %v", opts.LocalMounts)
			}
		})
	}
}

func TestNormalizeHTTPURL(t *testing.T) {
	if got, err := NormalizeHTTPURL("https://files.example.com/a/ctx.tar.gz?token=x#frag"); err != nil ||
		got != "https://files.example.com/a/ctx.tar.gz?token=x" {
		t.Fatalf("fragment must be dropped: %q, %v", got, err)
	}
	for _, bad := range []string{"/local/ctx.tar", "ftp://example.com/ctx.tar", "git@example.com:ctx.tar"} {
		if _, err := NormalizeHTTPURL(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}
