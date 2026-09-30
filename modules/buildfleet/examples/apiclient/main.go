// apiclient builds via the buildfleetd API over native gRPC and prints
// the live event stream.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"golang.org/x/net/http2"

	"connectrpc.com/connect"

	buildfleetv1 "github.com/funcx27/kite/modules/buildfleet/api/buildfleetv1"
	buildfleetv1connect "github.com/funcx27/kite/modules/buildfleet/api/buildfleetv1/buildfleetv1connect"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: apiclient <context.tar>")
	}
	tar, err := os.ReadFile(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	addr := "http://127.0.0.1:8484"
	if len(os.Args) > 2 {
		addr = os.Args[2]
	}
	output := "192.168.107.1:5000/demo-grpc:latest"
	if len(os.Args) > 3 {
		output = os.Args[3]
	}

	// gRPC over cleartext needs an h2c-capable client.
	h2c := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			// h2c: plain TCP, no TLS handshake.
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
	c := buildfleetv1connect.NewBuildServiceClient(h2c, addr,
		connect.WithGRPC(), // native gRPC protocol (HTTP/2 + protobuf)
	)
	stream, err := c.Build(context.Background(), connect.NewRequest(&buildfleetv1.BuildRequest{
		Context:   &buildfleetv1.BuildRequest_ContextTar{ContextTar: tar},
		OutputRef: output,
	}))
	if err != nil {
		log.Fatal(err)
	}
	for stream.Receive() {
		switch e := stream.Msg().Event.(type) {
		case *buildfleetv1.BuildEvent_Vertex:
			fmt.Printf("[%s] %s\n", e.Vertex.Status, e.Vertex.Name)
		case *buildfleetv1.BuildEvent_Log:
			fmt.Printf("    %s", e.Log.Data)
		case *buildfleetv1.BuildEvent_Result:
			fmt.Printf("RESULT %s %s (attempts=%d)\n", e.Result.ImageRef, e.Result.Digest, e.Result.Attempts)
		case *buildfleetv1.BuildEvent_Error:
			fmt.Printf("ERROR %s\n", e.Error)
		}
	}
	if err := stream.Err(); err != nil {
		log.Fatal(err)
	}
}
