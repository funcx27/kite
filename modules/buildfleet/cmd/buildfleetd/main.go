// buildfleetd serves the buildfleet API over Connect (gRPC, gRPC-Web and
// HTTP/JSON on one port) with a mixed docker/k8s runtime pool driven by a
// YAML config file.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"

	"gopkg.in/yaml.v3"

	"github.com/funcx27/kite/modules/buildfleet/api"
	buildfleetv1 "github.com/funcx27/kite/modules/buildfleet/api/buildfleetv1"
	buildfleetv1connect "github.com/funcx27/kite/modules/buildfleet/api/buildfleetv1/buildfleetv1connect"
	"github.com/funcx27/kite/modules/buildfleet/app"
	"github.com/funcx27/kite/modules/buildfleet/config"
	"github.com/funcx27/kite/modules/buildfleet/resource"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "status" {
		os.Exit(runStatus(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "cachestat" {
		os.Exit(runCacheStat(os.Args[2:]))
	}
	cfgPath := flag.String("config", "", "config file path (empty = embedded defaults, overlaid when given)")
	addrOverride := flag.String("addr", "", "override config addr")
	printCfg := flag.Bool("print-config", false, "print the effective config and exit")
	flag.Parse()

	if *printCfg {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			slog.Error("config load failed", "err", err)
			os.Exit(1)
		}
		b, err := yaml.Marshal(cfg)
		if err != nil {
			slog.Error("marshal config failed", "err", err)
			os.Exit(1)
		}
		os.Stdout.Write(b)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}
	if *addrOverride != "" {
		cfg.Addr = *addrOverride
	}

	a, err := app.Setup(context.Background(), cfg)
	if err != nil {
		slog.Error("setup failed", "err", err)
		os.Exit(1)
	}

	var apiOpts []api.ServerOption
	if a.CacheStatFn != nil {
		apiOpts = append(apiOpts, api.WithCacheStat(a.CacheStatFn))
	}
	path, handler := buildfleetv1connect.NewBuildServiceHandler(api.NewServer(a.Fleet, a.Store, apiOpts...))
	mux := http.NewServeMux()
	mux.Handle(path, handler)

	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true) // native gRPC on cleartext
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		Protocols:         &protos,
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("buildfleetd listening", "addr", cfg.Addr, "protocols", "gRPC+gRPC-Web+HTTP/JSON", "providers", strings.Join(providerTypes(cfg), " -> "))
	slog.Error("server exited", "err", srv.ListenAndServe())
}

func providerTypes(cfg *config.Config) []string {
	types := make([]string, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		types = append(types, p.Name)
	}
	return types
}

// runStatus is a thin client for the daemon's Status RPC:
// buildfleetd status [-addr host:port].
func runStatus(args []string) int {
	client, addr, code := dialDaemon("status", args)
	if client == nil {
		return code
	}
	resp, err := client.Status(context.Background(), connect.NewRequest(&buildfleetv1.StatusRequest{}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: rpc %s: %v\n", addr, err)
		return 1
	}
	out := resp.Msg

	fmt.Printf("%-34s %-14s %-8s %5s %8s %7s  %s\n",
		"RUNTIME", "PROVIDER", "STATE", "CPU", "MEMORY", "IDLE", "ADDR")
	for _, r := range out.Runtimes {
		idle := "-"
		if r.IdleSeconds > 0 {
			idle = fmt.Sprintf("%ds", r.IdleSeconds)
		}
		fmt.Printf("%-34s %-14s %-8s %5.4g %8s %7s  %s\n",
			r.Id, or(r.Provider, "-"), r.State, r.Cpu,
			resource.Bytes(r.MemoryBytes), idle, r.Addr)
	}
	if len(out.Tasks) > 0 {
		fmt.Printf("\nIN-FLIGHT BUILDS\n%-34s %-4s %-12s %s\n", "RUNTIME", "TRY", "OUTPUT", "SOURCE")
		for _, t := range out.Tasks {
			started := time.Unix(t.StartedUnix, 0).Format("15:04:05")
			fmt.Printf("%-34s %-4d %-12s %s (since %s)\n",
				t.RuntimeId, t.Attempt, t.OutputRef, t.Source, started)
		}
	}
	if len(out.Runtimes) == 0 {
		fmt.Println("(no runtimes)")
	}
	return 0
}

// dialDaemon parses the shared -addr flag for query subcommands.
func dialDaemon(name string, args []string) (buildfleetv1connect.BuildServiceClient, string, int) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8484", "daemon address")
	if err := fs.Parse(args); err != nil {
		return nil, "", 2
	}
	return buildfleetv1connect.NewBuildServiceClient(&http.Client{}, "http://"+*addr), *addr, 0
}

// humanBytes renders sizes with one fraction digit (27.0Mi, 1.5Gi).
func humanBytes(n int64) string {
	switch {
	case n >= int64(resource.Giga):
		return fmt.Sprintf("%.1fGi", float64(n)/float64(resource.Giga))
	case n >= int64(resource.Mega):
		return fmt.Sprintf("%.1fMi", float64(n)/float64(resource.Mega))
	case n >= int64(resource.Kilo):
		return fmt.Sprintf("%.1fKi", float64(n)/float64(resource.Kilo))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// runCacheStat prints per-project cache registry usage:
// buildfleetd cachestat [-addr host:port].
func runCacheStat(args []string) int {
	client, addr, code := dialDaemon("cachestat", args)
	if client == nil {
		return code
	}
	resp, err := client.CacheStat(context.Background(), connect.NewRequest(&buildfleetv1.CacheStatRequest{}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "cachestat: rpc %s: %v\n", addr, err)
		return 1
	}
	msg := resp.Msg
	fmt.Printf("%-14s %-28s %5s %7s %10s %9s\n",
		"PROVIDER", "PROJECT", "TAGS", "LAYERS", "EXCLUSIVE", "SHARED")
	for _, p := range msg.Projects {
		fmt.Printf("%-14s %-28s %5d %7d %10s %9s\n",
			p.Provider, p.Project, p.Tags, p.Layers,
			humanBytes(p.ExclusiveBytes), humanBytes(p.SharedBytes))
	}
	fmt.Printf("TOTAL: %s\n", humanBytes(msg.TotalBytes))
	if len(msg.Projects) == 0 {
		fmt.Println("(no cache projects)")
	}
	return 0
}
