package builder

import (
	"context"
	"testing"
)

func TestNormalizeGitURL(t *testing.T) {
	tests := []struct {
		name          string
		url, ref, sub string
		want          string
		wantErr       bool
	}{
		{"plain https gets .git", "https://git.example.com/team/app", "", "", "https://git.example.com/team/app.git", false},
		{"already .git untouched", "https://git.example.com/team/app.git", "", "", "https://git.example.com/team/app.git", false},
		{"github without .git untouched", "https://github.com/foo/bar", "", "", "https://github.com/foo/bar", false},
		{"with ref", "https://git.example.com/team/app.git", "v1", "", "https://git.example.com/team/app.git#v1", false},
		{"with subdir only", "https://git.example.com/team/app.git", "", "app", "https://git.example.com/team/app.git#:app", false},
		{"with ref and subdir", "https://git.example.com/team/app.git", "main", "sub", "https://git.example.com/team/app.git#main:sub", false},
		{"ssh passthrough", "ssh://git@example.com/team/app.git", "", "", "ssh://git@example.com/team/app.git", false},
		{"local path rejected", "/tmp/repo", "", "", "", true},
		{"file rejected", "file:///tmp/repo", "", "", "", true},
		{"git proto rejected", "git://example.com/app.git", "", "", "", true},
	}
	for _, tt := range tests {
		got, err := NormalizeGitURL(tt.url, tt.ref, tt.sub)
		if (err != nil) != tt.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", tt.name, err, tt.wantErr)
		}
		if err == nil && got != tt.want {
			t.Fatalf("%s: got %q want %q", tt.name, got, tt.want)
		}
	}
}

func TestSolveOptGitURL(t *testing.T) {
	b := &Builder{}
	in := SolveInput{
		GitURL:     "https://git.example.com/team/app.git#v1:app",
		Dockerfile: "Dockerfile",
		OutputRef:  "reg.local:5000/x:1",
		GitSecrets: map[string]SourceSecret{
			"git.example.com": {Header: "Basic dXNlcjpwYXNz"},
		},
	}
	opts, err := b.solveOpt(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got := opts.FrontendAttrs["context"]; got != in.GitURL {
		t.Fatalf("context attr: %q", got)
	}
	if opts.LocalMounts != nil {
		t.Fatal("git context must not bind local mounts")
	}
	if len(opts.Session) == 0 {
		t.Fatal("git secrets session missing")
	}
}

func TestSolveOptLocalDirStillWorks(t *testing.T) {
	b := &Builder{}
	opts, err := b.solveOpt(context.Background(), SolveInput{ContextDir: ".", Dockerfile: "Dockerfile"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.LocalMounts == nil || opts.LocalMounts["context"] == nil {
		t.Fatal("local context lost")
	}
	if _, has := opts.FrontendAttrs["context"]; has {
		t.Fatal("context attr must not be set for local builds")
	}
}
