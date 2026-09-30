package store

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stringReader(s string) io.Reader { return strings.NewReader(s) }

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestPushPullRoundTrip(t *testing.T) {
	s := NewLocalStore(t.TempDir())
	dig, err := s.Push(context.Background(), stringReader("hello buildfleet"))
	if err != nil {
		t.Fatal(err)
	}
	if dig != "sha256:"+sha256Hex("hello buildfleet") {
		t.Fatalf("unexpected digest %s", dig)
	}
	rc, err := s.Pull(context.Background(), dig)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b := make([]byte, 32)
	n, _ := rc.Read(b)
	if string(b[:n]) != "hello buildfleet" {
		t.Fatalf("got %q", b[:n])
	}
}

func TestPushDedup(t *testing.T) {
	s := NewLocalStore(t.TempDir())
	d1, _ := s.Push(context.Background(), stringReader("same"))
	d2, _ := s.Push(context.Background(), stringReader("same"))
	if d1 != d2 {
		t.Fatalf("same content must dedup: %s vs %s", d1, d2)
	}
}

func TestPullToFile(t *testing.T) {
	s := NewLocalStore(t.TempDir())
	dig, _ := s.Push(context.Background(), stringReader("filebody"))
	dir := t.TempDir()
	if err := s.PullToFile(context.Background(), dig, filepath.Join(dir, "ctx.tar")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "ctx.tar"))
	if string(got) != "filebody" {
		t.Fatalf("got %q", got)
	}
}

func TestPullToDir(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	mustWrite := func(h *tar.Header, content string) {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len("FROM scratch"))}, "FROM scratch")
	mustWrite(&tar.Header{Name: "sub", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	mustWrite(&tar.Header{Name: "sub/app.txt", Mode: 0o644, Size: 4}, "body")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	s := NewLocalStore(t.TempDir())
	dig, err := s.Push(context.Background(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "ctx")
	if err := s.PullToDir(context.Background(), dig, dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if string(got) != "FROM scratch" {
		t.Fatalf("Dockerfile: got %q", got)
	}
	got, _ = os.ReadFile(filepath.Join(dir, "sub", "app.txt"))
	if string(got) != "body" {
		t.Fatalf("sub/app.txt: got %q", got)
	}
}
