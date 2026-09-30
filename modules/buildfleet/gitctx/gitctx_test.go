package gitctx

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// originRepo creates a local git repo with Dockerfile at root and app/inner.txt.
func originRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(rel); err != nil {
			t.Fatal(err)
		}
	}
	write("Dockerfile", "FROM scratch\n")
	write("app/inner.txt", "inner")
	if _, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t"},
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func untarNames(t *testing.T, tarPath string) map[string]string {
	t.Helper()
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	out := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = string(b)
	}
}

func TestFetchHead(t *testing.T) {
	origin := originRepo(t)
	res, err := Fetch(context.Background(), Source{URL: origin})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.TarPath)
	if len(res.Commit) != 40 {
		t.Fatalf("commit: %q", res.Commit)
	}
	files := untarNames(t, res.TarPath)
	if files["Dockerfile"] != "FROM scratch\n" {
		t.Fatalf("dockerfile: %v", files)
	}
	if files["app/inner.txt"] != "inner" {
		t.Fatalf("app file: %v", files)
	}
	for name := range files {
		if name == ".git" || strings.HasPrefix(name, ".git/") {
			t.Fatalf(".git leaked into tar: %s", name)
		}
	}
}

func TestFetchDeterministic(t *testing.T) {
	origin := originRepo(t)
	r1, err := Fetch(context.Background(), Source{URL: origin})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(r1.TarPath)
	r2, err := Fetch(context.Background(), Source{URL: origin})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(r2.TarPath)

	h1, err := os.ReadFile(r1.TarPath)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := os.ReadFile(r2.TarPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(h1) != string(h2) {
		t.Fatal("same commit must produce identical tar bytes")
	}
}

func TestFetchSubdirAndRef(t *testing.T) {
	origin := originRepo(t)
	// tag the initial commit as v1
	repo, err := git.PlainOpen(origin)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateTag("v1", head.Hash(), &git.CreateTagOptions{
		Message: "v1",
		Tagger:  &object.Signature{Name: "t", Email: "t@t"},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := Fetch(context.Background(), Source{URL: origin, Ref: "v1", Subdir: "app"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.TarPath)
	files := untarNames(t, res.TarPath)
	if _, ok := files["inner.txt"]; !ok {
		t.Fatalf("subdir root not applied: %v", files)
	}
	if len(files) != 1 {
		t.Fatalf("unexpected files: %v", files)
	}
}

func TestFetchBySHA(t *testing.T) {
	origin := originRepo(t)
	repo, err := git.PlainOpen(origin)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Fetch(context.Background(), Source{URL: origin, Ref: head.Hash().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.TarPath)
	if res.Commit != head.Hash().String() {
		t.Fatalf("commit: got %s want %s", res.Commit, head.Hash())
	}
	files := untarNames(t, res.TarPath)
	if files["Dockerfile"] != "FROM scratch\n" {
		t.Fatalf("files: %v", files)
	}
}

func TestPlanClone(t *testing.T) {
	origin := originRepo(t)
	repo, _ := git.PlainOpen(origin)
	head, _ := repo.Head()

	tests := []struct {
		name      string
		ref       string
		wantName  plumbing.ReferenceName
		wantShalw bool
		wantErr   bool
	}{
		{"head empty", "", "", true, false},
		{"head literal", "HEAD", "", true, false},
		{"branch", "master", plumbing.NewBranchReferenceName("master"), true, false},
		{"sha", head.Hash().String(), "", false, false},
		{"unknown", "nope", "", false, true},
	}
	for _, tt := range tests {
		name, shallow, err := planClone(origin, tt.ref, nil)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("%s: want error", tt.name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if name != tt.wantName || shallow != tt.wantShalw {
			t.Fatalf("%s: got (%s, %v) want (%s, %v)", tt.name, name, shallow, tt.wantName, tt.wantShalw)
		}
	}
}

func TestResolveCommit(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wt.AddGlob("."); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("init", &git.CommitOptions{Author: defaultSignature()}); err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	want := head.Hash().String()

	if got := ResolveCommit(Source{URL: dir, Ref: "master"}); got != want {
		t.Fatalf("by branch: got %q want %q", got, want)
	}
	if got := ResolveCommit(Source{URL: dir}); got != want { // empty ref = HEAD
		t.Fatalf("HEAD: got %q want %q", got, want)
	}
	if got := ResolveCommit(Source{URL: dir, Ref: want}); got != want { // raw hash
		t.Fatalf("by hash: got %q want %q", got, want)
	}
	if got := ResolveCommit(Source{URL: dir, Ref: "no-such-branch"}); got != "" {
		t.Fatalf("unknown ref: want empty, got %q", got)
	}
}

func defaultSignature() *object.Signature {
	return &object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0)}
}
