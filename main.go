package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "net/http/pprof"

	"github.com/zxh326/kite/pkg/clusteragent"
	"github.com/zxh326/kite/pkg/common"
	"github.com/zxh326/kite/pkg/version"
	"k8s.io/klog/v2"
)

func main() {
	klog.InitFlags(nil)
	if len(os.Args) > 1 && os.Args[1] == "cluster-agent" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		err := clusteragent.Run(ctx, os.Args[2:])
		stop()
		if err != nil {
			log.Fatalf("Failed to run cluster agent: %v", err)
		}
		return
	}
	flag.Parse()
	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	appCtx, cancelApp := context.WithCancel(context.Background())

	cm, buildfleet, err := initializeApp(appCtx)
	if err != nil {
		cancelApp()
		log.Fatalf("Failed to initialize app: %v", err)
	}
	defer cancelApp()

	srv := &http.Server{
		Addr:              ":" + common.Port,
		Handler:           buildEngine(cm, buildfleet).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.Fatalf("Failed to start server: %v", err)
		}
	}()
	klog.Infof("Kite server started on port %s", common.Port)
	klog.Infof("Version: %s, Build Date: %s, Commit: %s",
		version.Version, version.BuildDate, version.CommitID)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	klog.Info("Shutting down server...")
	cancelApp()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		// SSE build streams routinely outlive this budget; exiting here
		// would skip the buildfleet drain below, so log and continue.
		klog.Errorf("Failed to shutdown server: %v", err)
	}
	if buildfleet != nil {
		// Drain runs once Shutdown has returned: streams had the shutdown
		// budget to finish, and instances the pool already marked
		// Draining/Failed are disposed. Fresh context: appCtx is cancelled
		// above and the shutdown budget may be spent.
		drainCtx, cancelDrain := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelDrain()
		if err := buildfleet.Close(drainCtx); err != nil {
			klog.Errorf("Failed to drain buildfleet: %v", err)
		}
	}
}
