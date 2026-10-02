package main

import (
	"context"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/zxh326/kite/internal"
	"github.com/zxh326/kite/pkg/buildfleetadapter"
	"github.com/zxh326/kite/pkg/cluster"
	"github.com/zxh326/kite/pkg/common"
	"github.com/zxh326/kite/pkg/credentialsadapter"
	"github.com/zxh326/kite/pkg/middleware"
	"github.com/zxh326/kite/pkg/model"
	"github.com/zxh326/kite/pkg/rbac"
	"github.com/zxh326/kite/pkg/scheduler"
	"github.com/zxh326/kite/pkg/templates"
	"k8s.io/klog/v2"
)

func initializeApp(ctx context.Context) (*cluster.ClusterManager, *buildfleetadapter.Deps, *credentialsadapter.Deps, error) {
	common.LoadEnvs()
	if klog.V(1).Enabled() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	model.InitDB()
	if _, err := model.GetGeneralSetting(); err != nil {
		klog.Warningf("Failed to load general setting: %v", err)
	}

	rbac.InitRBAC()
	templates.InitTemplates()
	internal.LoadConfigFromFile(common.ConfigFilePath)
	if common.ConfigFilePath == "" {
		internal.LoadConfigFromEnv()
	}

	cm, err := cluster.NewClusterManager()
	if err != nil {
		return nil, nil, nil, err
	}
	// The credentials adapter must exist before the buildfleet runtime: the
	// module's Resolver flows into the buildfleet adapter at construction.
	credDeps := credentialsadapter.New()
	var buildfleet *buildfleetadapter.Deps
	if node := internal.BuildFleetConfig(); node != nil {
		// Fail fast: a present but broken buildfleet section must not
		// silently start kite without the module.
		buildfleet, err = buildfleetadapter.New(ctx, node, credDeps)
		if err != nil {
			panic("failed to initialize buildfleet module: " + err.Error())
		}
	}
	if err := internal.StartConfigWatcher(ctx, common.ConfigFilePath); err != nil {
		klog.Warningf("Failed to watch config file: %v", err)
	}
	scheduler.Start(ctx, cm)
	return cm, buildfleet, credDeps, nil
}

func buildEngine(cm *cluster.ClusterManager, buildfleet *buildfleetadapter.Deps, credDeps *credentialsadapter.Deps) *gin.Engine {
	r := gin.New()
	middleware.ConfigureRawPathRouting(r)
	configureTrustedProxies(r)
	r.Use(middleware.Metrics())
	if !common.DisableGZIP {
		klog.Info("GZIP compression is enabled")
		r.Use(gzip.Gzip(gzip.DefaultCompression, gzip.WithExcludedPaths([]string{"/metrics"})))
	}
	r.Use(gin.Recovery())
	r.Use(middleware.Logger())
	r.Use(middleware.DevCORS(common.CORSAllowedOrigins))

	base := r.Group(common.Base)
	setupAPIRouter(base, cm, buildfleet, credDeps)
	setupStatic(r)

	return r
}

func configureTrustedProxies(r *gin.Engine) {
	var trustedProxies []string
	if len(common.TrustedProxies) > 0 {
		trustedProxies = common.TrustedProxies
	}
	if err := r.SetTrustedProxies(trustedProxies); err != nil {
		klog.Fatalf("Failed to configure trusted proxies: %v", err)
	}
}
