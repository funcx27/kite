package main

// Platform module wiring (application / workflow / buildfleet / credentials /
// kcross). The console-shaped app.go, main.go and routes.go stay byte-aligned
// with upstream and reach this code only through one-line hooks.

import (
	"context"
	"time"

	"github.com/funcx27/kite/modules/application"
	"github.com/funcx27/kite/modules/workflow"
	"github.com/gin-gonic/gin"
	"github.com/zxh326/kite/internal"
	"github.com/zxh326/kite/pkg/applicationadapter"
	"github.com/zxh326/kite/pkg/auth"
	"github.com/zxh326/kite/pkg/buildfleetadapter"
	"github.com/zxh326/kite/pkg/certificate"
	"github.com/zxh326/kite/pkg/cluster"
	"github.com/zxh326/kite/pkg/credentialsadapter"
	"github.com/zxh326/kite/pkg/kcross"
	"github.com/zxh326/kite/pkg/model"
	"github.com/zxh326/kite/pkg/users"
	"github.com/zxh326/kite/pkg/workflowadapter"
	"k8s.io/klog/v2"
)

// platformModules carries adapter state between the one-line hooks in the
// upstream-shaped entry files, so their function signatures stay upstream's.
var platformModules struct {
	buildfleet *buildfleetadapter.Deps
	credDeps   *credentialsadapter.Deps
	appDeps    *applicationadapter.Deps
	wfModule   *workflow.Module
}

// initPlatformModules builds the startup-ordered modules. The credentials
// adapter must exist before the buildfleet runtime: the module's Resolver
// flows into the buildfleet adapter at construction. Hook: called from
// initializeApp once the cluster manager exists.
func initPlatformModules(ctx context.Context, cm *cluster.ClusterManager) {
	credDeps := credentialsadapter.New()
	var buildfleet *buildfleetadapter.Deps
	if node := internal.BuildFleetConfig(); node != nil {
		// Fail fast: a present but broken buildfleet section must not
		// silently start kite without the module.
		b, err := buildfleetadapter.New(ctx, cm, node, credDeps)
		if err != nil {
			panic("failed to initialize buildfleet module: " + err.Error())
		}
		buildfleet = b
	}
	platformModules.credDeps = credDeps
	platformModules.buildfleet = buildfleet
}

// drainPlatformModules disposes the buildfleet pool at shutdown. Hook: called
// from main after the HTTP shutdown budget is spent.
func drainPlatformModules() {
	if platformModules.buildfleet == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := platformModules.buildfleet.Close(ctx); err != nil {
		klog.Errorf("Failed to drain buildfleet: %v", err)
	}
}

// initPlatformRoutes builds the route-level adapters, migrates their tables
// and resumes runs interrupted by a previous process (design §6 重启恢复).
// Hook: called from registerProtectedRoutes before the cluster route groups.
func initPlatformRoutes(cm *cluster.ClusterManager) {
	platformModules.appDeps = applicationadapter.NewDeps(cm, platformModules.credDeps)
	if err := applicationadapter.Migrate(model.DB); err != nil {
		panic("failed to migrate application module: " + err.Error())
	}
	if err := credentialsadapter.Migrate(model.DB); err != nil {
		panic("failed to migrate credentials module: " + err.Error())
	}
	// A typed-nil *buildfleetadapter.Deps would arrive as a non-nil
	// interface: convert explicitly so BuildImage sees "not configured".
	var fleetCap workflowadapter.FleetBuilder
	if platformModules.buildfleet != nil {
		fleetCap = platformModules.buildfleet
	}
	// appDeps backs both the application module API and the workflow
	// adapter's DeployApp facade (same Deps wiring, same audit attribution).
	wfDeps := workflowadapter.NewDeps(cm, fleetCap, platformModules.credDeps, platformModules.appDeps)
	if err := workflowadapter.Migrate(model.DB); err != nil {
		panic("failed to migrate workflow module: " + err.Error())
	}
	wfModule := workflow.New(wfDeps)
	if err := wfModule.Start(); err != nil {
		panic("failed to start workflow module: " + err.Error())
	}
	platformModules.wfModule = wfModule
}

// registerPlatformModuleRoutes mounts the module APIs. Hook: called from
// registerClusterProtectedRoutes before the RBAC middleware — the module URLs
// are non-resource shapes and authorize themselves (design §8).
func registerPlatformModuleRoutes(api *gin.RouterGroup) {
	application.RegisterRoutes(api, platformModules.appDeps)
	if platformModules.buildfleet != nil {
		platformModules.buildfleet.RegisterRoutes(api.Group("/buildfleet"))
	}
	platformModules.wfModule.RegisterRoutes(api)
	platformModules.credDeps.RegisterRoutes(api)
	// Read-only certificate metadata for the application wizard's picker.
	certificate.RegisterModuleRoutes(api.Group("/certificates"))
}

// registerPlatformPublicRoutes mounts the routes that must sit outside the
// session/RBAC middleware: the public workflow webhook (design §7 — the token
// in the path is the credential, the module owns the path shape) and the
// assignee-picker user directory (RequireAuth only; the handler enforces
// admin or wildcard workflows access itself).
func registerPlatformPublicRoutes(r *gin.RouterGroup, authHandler *auth.AuthHandler) {
	platformModules.wfModule.RegisterPublicRoutes(r)
	v1UserGroup := r.Group("/api/v1/users")
	v1UserGroup.GET("/directory", authHandler.RequireAuth(), users.ListUserDirectory)
}

// registerPlatformAdminRoutes mounts the kcross admin API (settings,
// discovery, bindings — all implemented in pkg/kcross) and the certificate
// management API (pkg/certificate).
func registerPlatformAdminRoutes(adminAPI *gin.RouterGroup) {
	kcrossAPI := adminAPI.Group("/kcross")
	kcross.RegisterRoutes(kcrossAPI)
	certificate.RegisterAdminRoutes(adminAPI.Group("/certificates"))
}
