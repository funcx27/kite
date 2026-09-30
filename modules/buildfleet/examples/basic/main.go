// Package main demonstrates the minimal buildfleet flow: push a build
// context tarball to the local store, provision pooled buildkitd runtimes
// as docker containers, and solve a Dockerfile, pushing by digest.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/funcx27/kite/modules/buildfleet/fleet"
	"github.com/funcx27/kite/modules/buildfleet/provider/docker"
	"github.com/funcx27/kite/modules/buildfleet/registry"
	"github.com/funcx27/kite/modules/buildfleet/resource"
	"github.com/funcx27/kite/modules/buildfleet/runtime"
	"github.com/funcx27/kite/modules/buildfleet/store"
)

func main() {
	ctx := context.Background()

	tarball := "context.tar"
	if len(os.Args) > 1 {
		tarball = os.Args[1]
	}

	st := store.NewLocalStore("/tmp/buildfleet-store")
	f, err := os.Open(tarball)
	if err != nil {
		log.Fatal(err)
	}
	ctxDigest, err := st.Push(ctx, f)
	f.Close()
	if err != nil {
		log.Fatal(err)
	}

	image := os.Getenv("BUILDFLEET_IMAGE")
	if image == "" {
		image = "moby/buildkit:master-rootless"
	}
	prov, err := docker.New(docker.Config{
		Total:      resource.Resources{CPU: 8, Memory: 16 * resource.Giga},
		Image:      image,
		Registries: registry.Table{{Host: "192.168.107.1:5000", HTTP: true}},
		Privileged: true,
	})
	if err != nil {
		log.Fatal(err)
	}
	pool := runtime.NewPool(prov)

	res, err := fleet.New(pool, nil /* default builder */, fleet.WithStore(st)).Build(ctx, fleet.Request{
		ContextDigest: ctxDigest,
		Dockerfile:    "Dockerfile",
		OutputRef:     "192.168.107.1:5000/demo:latest",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("pushed", res.ImageRef, res.Digest)
}
