// httpclient builds from an http file source via the buildfleetd API
// (native gRPC) and prints the live event stream. Driven by a YAML config
// file.
//
// Usage: httpclient -config httpclient.yaml
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"gopkg.in/yaml.v3"

	buildfleetv1 "github.com/funcx27/kite/modules/buildfleet/api/buildfleetv1"
	buildfleetv1connect "github.com/funcx27/kite/modules/buildfleet/api/buildfleetv1/buildfleetv1connect"
)

type Config struct {
	Server string `yaml:"server"`
	// Dockerfile is a context-relative path, or inline content when it
	// contains whitespace (Dockerfile instructions always do; paths
	// never do) — inline content overrides the context's file.
	Dockerfile string  `yaml:"dockerfile"`
	HTTP       HTTPCfg `yaml:"http"`
	Push       PushCfg `yaml:"push"`
	// Provider pins the build to a named provider; empty = preference order.
	Provider  string `yaml:"provider"`
	Resources ResCfg `yaml:"resources"`
}

type HTTPCfg struct {
	// URL points at a tar/tar.gz archive or a single file.
	URL  string `yaml:"url"`
	Mode string `yaml:"mode"` // server | runtime
	// Auth is "user:pass" basic auth for the file host. Prefer AuthEnv in
	// shared environments so secrets stay out of files. Both modes resolve
	// it daemon-side (server: download auth; runtime: session secret for
	// buildkitd's http source).
	Auth    string `yaml:"auth"`
	AuthEnv string `yaml:"authEnv"`
}

type PushCfg struct {
	Ref     string `yaml:"ref"` // image ref to push; empty builds without push
	Auth    string `yaml:"auth"`
	AuthEnv string `yaml:"authEnv"`
}

type ResCfg struct {
	CPU    float64 `yaml:"cpu"`    // cores; 0 = smallest profile tier
	Memory string  `yaml:"memory"` // quantity e.g. 8Gi; empty = tier default
}

func (c *Config) applyDefaults() {
	if c.Server == "" {
		c.Server = "http://127.0.0.1:8484"
	}
	if c.Dockerfile == "" {
		c.Dockerfile = "Dockerfile"
	}
	if c.HTTP.Mode == "" {
		c.HTTP.Mode = "server"
	}
}

func main() {
	cfgPath := flag.String("config", "httpclient.yaml", "client config file")
	flag.Parse()

	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		log.Fatalf("parse %s: %v", *cfgPath, err)
	}
	cfg.applyDefaults()
	if cfg.HTTP.URL == "" {
		log.Fatal("http.url is required")
	}

	// gRPC over cleartext needs an h2c-capable client.
	h2c := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
	c := buildfleetv1connect.NewBuildServiceClient(h2c, cfg.Server, connect.WithGRPC())

	var dfContent string
	if strings.ContainsAny(strings.TrimSpace(cfg.Dockerfile), " \t\n") {
		// Inline content (multiline or any instruction with args); a
		// bare token is a path inside the context.
		dfContent = cfg.Dockerfile
	}
	req := &buildfleetv1.BuildRequest{
		Context: &buildfleetv1.BuildRequest_Http{Http: &buildfleetv1.HTTPContext{
			Url:     cfg.HTTP.URL,
			Mode:    cfg.HTTP.Mode,
			AuthEnv: cfg.HTTP.AuthEnv,
		}},
		OutputRef:         cfg.Push.Ref,
		Dockerfile:        cfg.Dockerfile,
		DockerfileContent: dfContent,
		Resources: &buildfleetv1.Resources{
			Cpu:    cfg.Resources.CPU,
			Memory: cfg.Resources.Memory,
		},
		Provider: cfg.Provider,
	}
	fmt.Printf("BUILD %s (mode=%s, fetch %s) -> %s\n",
		cfg.HTTP.URL, cfg.HTTP.Mode,
		map[string]string{"server": "daemon", "runtime": "buildkitd"}[cfg.HTTP.Mode],
		cfg.Push.Ref)
	if cfg.Provider != "" {
		fmt.Printf("PINNED to provider %s\n", cfg.Provider)
	}

	addCred := func(kind, match, auth, authEnv string) {
		v := auth
		if v == "" && authEnv != "" {
			v = os.Getenv(authEnv)
			if v == "" {
				log.Fatalf("%s auth env %s not set", kind, authEnv)
			}
		}
		if v == "" {
			return
		}
		user, pass, _ := strings.Cut(v, ":")
		req.Credentials = append(req.Credentials, &buildfleetv1.Credential{
			Kind: kind, Match: match, Username: user, Password: pass,
		})
	}
	// http source downloads share the git credential kind: both match the
	// download host.
	addCred("git", hostOf(cfg.HTTP.URL), cfg.HTTP.Auth, cfg.HTTP.AuthEnv)
	addCred("registry", hostOf(cfg.Push.Ref), cfg.Push.Auth, cfg.Push.AuthEnv)

	stream, err := c.Build(context.Background(), connect.NewRequest(req))
	if err != nil {
		log.Fatal(err)
	}
	for stream.Receive() {
		switch e := stream.Msg().Event.(type) {
		case *buildfleetv1.BuildEvent_Vertex:
			fmt.Printf("[%s] %s\n", e.Vertex.Status, e.Vertex.Name)
		case *buildfleetv1.BuildEvent_Runtime:
			fmt.Printf("RUNTIME %s (provider=%s, addr=%s, attempt=%d)\n",
				e.Runtime.RuntimeId, e.Runtime.Provider, e.Runtime.Addr, e.Runtime.Attempt)
		case *buildfleetv1.BuildEvent_Log:
			fmt.Printf("    %s", e.Log.Data)
		case *buildfleetv1.BuildEvent_Result:
			fmt.Printf("RESULT %s %s (attempts=%d runtime=%s provider=%s)\n",
				e.Result.ImageRef, e.Result.Digest, e.Result.Attempts, e.Result.RuntimeId, e.Result.Provider)
		case *buildfleetv1.BuildEvent_Error:
			fmt.Printf("ERROR %s\n", e.Error)
		}
	}
	if err := stream.Err(); err != nil {
		log.Fatal(err)
	}
}

// hostOf extracts the host from an image ref or URL; url.Parse cannot
// handle bare refs (no scheme), and using the full ref as the credential
// match string breaks the server's host-prefix matching.
func hostOf(ref string) string {
	ref = strings.TrimPrefix(strings.TrimPrefix(ref, "https://"), "http://")
	first := strings.SplitN(ref, "/", 2)[0]
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first
	}
	return "docker.io"
}
