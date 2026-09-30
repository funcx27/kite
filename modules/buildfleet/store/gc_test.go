package store

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRefcountProtectsFromGC(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalStore(dir)
	dig, _ := s.Push(context.Background(), stringReader("protected"))

	inc, err := s.AcquireRef(dig)
	if err != nil {
		t.Fatal(err)
	}

	gc := NewGC(s, 1*time.Millisecond)
	gc.SetNow(func() time.Time { return time.Now().Add(time.Hour) }) // force-expire
	if err := gc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.path(dig)); err != nil {
		t.Fatal("referenced blob must survive GC")
	}

	inc()
	gc.SetNow(func() time.Time { return time.Now().Add(2 * time.Hour) })
	if err := gc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.path(dig)); !os.IsNotExist(err) {
		t.Fatal("unreferenced expired blob must be collected")
	}
}
