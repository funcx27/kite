package httpctx

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"

	"strings"
	"testing"
)

// tarBytes builds an in-memory tar from name -> content entries.
func tarBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		if strings.HasSuffix(name, "/") {
			if err := tw.WriteHeader(&tar.Header{Name: strings.TrimSuffix(name, "/"), Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(content)), Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// readTar returns the tar's name -> content mapping.
func readTar(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]string{}
	tr := tar.NewReader(f)
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

func TestFetch(t *testing.T) {
	tests := []struct {
		name    string
		serve   http.HandlerFunc
		urlPath string
		want    map[string]string
	}{
		{
			name: "tar extracted",
			serve: func(w http.ResponseWriter, r *http.Request) {
				w.Write(tarBytes(t, map[string]string{"Dockerfile": "FROM scratch", "app/hello.txt": "hi"}))
			},
			want: map[string]string{"Dockerfile": "FROM scratch", "app/hello.txt": "hi"},
		},
		{
			name: "tar.gz extracted",
			serve: func(w http.ResponseWriter, r *http.Request) {
				gz := gzip.NewWriter(w)
				gz.Write(tarBytes(t, map[string]string{"Dockerfile": "FROM alpine"}))
				gz.Close()
			},
			want: map[string]string{"Dockerfile": "FROM alpine"},
		},
		{
			name:    "single file named from url",
			serve:   func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("just a file")) },
			urlPath: "/files/notes.txt",
			want:    map[string]string{"notes.txt": "just a file"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.serve)
			defer srv.Close()
			url := srv.URL + tt.urlPath
			if url == srv.URL {
				url += "/ctx.tar"
			}
			res, err := Fetch(context.Background(), Source{URL: url})
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(res.TarPath)
			if got := readTar(t, res.TarPath); len(got) != len(tt.want) {
				t.Fatalf("entries: %v, want %v", got, tt.want)
			} else {
				for name, content := range tt.want {
					if got[name] != content {
						t.Fatalf("%s: %q, want %q", name, got[name], content)
					}
				}
			}
		})
	}
}

func TestFetchDeterministic(t *testing.T) {
	body := tarBytes(t, map[string]string{"Dockerfile": "FROM scratch"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()

	tars := make([]string, 2)
	for i := range tars {
		res, err := Fetch(context.Background(), Source{URL: srv.URL + "/ctx.tar"})
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(res.TarPath)
		tars[i] = res.TarPath
	}
	a, err := os.ReadFile(tars[0])
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(tars[1])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("same bytes at the URL must yield identical context tars")
	}
}

func TestFetchAuth(t *testing.T) {
	tests := []struct {
		name string
		src  Source
	}{
		{name: "literal", src: Source{Username: "user", Password: "pass"}},
		{name: "auth env", src: Source{AuthEnv: "BF_HTTP_AUTH"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.src.AuthEnv != "" {
				t.Setenv(tt.src.AuthEnv, "user:pass")
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, pass, ok := r.BasicAuth()
				if !ok || user != "user" || pass != "pass" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Write(tarBytes(t, map[string]string{"Dockerfile": "FROM scratch"}))
			}))
			defer srv.Close()
			tt.src.URL = srv.URL + "/ctx.tar"
			res, err := Fetch(context.Background(), tt.src)
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(res.TarPath)
		})
	}
}

func TestFetchErrors(t *testing.T) {
	tests := []struct {
		name string
		url  func(srv *httptest.Server) string
	}{
		{name: "non-http scheme", url: func(*httptest.Server) string { return "ftp://example.com/ctx.tar" }},
		{name: "not a url", url: func(*httptest.Server) string { return "/local/path" }},
		{name: "http status", url: func(srv *httptest.Server) string { return srv.URL + "/missing" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			}))
			defer srv.Close()
			if _, err := Fetch(context.Background(), Source{URL: tt.url(srv)}); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestFetchAuthEnvNotSet(t *testing.T) {
	if _, err := Fetch(context.Background(), Source{URL: "http://example.com/ctx.tar", AuthEnv: "BF_HTTP_MISSING"}); err == nil ||
		!strings.Contains(err.Error(), "BF_HTTP_MISSING") {
		t.Fatalf("want missing-env error, got %v", err)
	}
}

// A malicious archive must not write outside the context dir: the
// Clean("/") rooting clamps every entry inside it, and the extracted
// tree keeps the benign entries intact.
func TestUntarTraversalClamped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarBytes(t, map[string]string{"Dockerfile": "FROM scratch", "../../evil.txt": "boo"}))
	}))
	defer srv.Close()
	res, err := Fetch(context.Background(), Source{URL: srv.URL + "/ctx.tar"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.TarPath)
	got := readTar(t, res.TarPath)
	if got["Dockerfile"] != "FROM scratch" || got["evil.txt"] != "boo" {
		t.Fatalf("want traversal entry clamped to context root, got %v", got)
	}
}

func TestSafeTarget(t *testing.T) {
	dir := "/tmp/ctx"
	for name, want := range map[string]string{
		"Dockerfile":     "/tmp/ctx/Dockerfile",
		"/abs/passwd":    "/tmp/ctx/abs/passwd",
		"a/../../escape": "/tmp/ctx/escape",
		"a/./b/../c":     "/tmp/ctx/a/c",
	} {
		if got := safeTarget(dir, name); got != want {
			t.Fatalf("safeTarget(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestURLFileName(t *testing.T) {
	for raw, want := range map[string]string{
		"http://h/files/app.tar.gz":   "app.tar.gz",
		"http://h/ctx":                "ctx",
		"http://h/":                   "download",
		"http://h/query?token=x#frag": "query",
	} {
		if got := urlFileName(raw); got != want {
			t.Fatalf("urlFileName(%q) = %q, want %q", raw, got, want)
		}
	}
}
