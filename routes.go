package main

import (
	"net/http"

	"github.com/funcx27/kite/modules/application"
	"github.com/funcx27/kite/modules/workflow"
	"github.com/gin-gonic/gin"
	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/zxh326/kite/pkg/ai"
	"github.com/zxh326/kite/pkg/apikeys"
	"github.com/zxh326/kite/pkg/applicationadapter"
	"github.com/zxh326/kite/pkg/audit"
	"github.com/zxh326/kite/pkg/auth"
	"github.com/zxh326/kite/pkg/buildfleetadapter"
	"github.com/zxh326/kite/pkg/cluster"
	"github.com/zxh326/kite/pkg/credentialsadapter"
	"github.com/zxh326/kite/pkg/helm"
	"github.com/zxh326/kite/pkg/images"
	"github.com/zxh326/kite/pkg/kcross"
	"github.com/zxh326/kite/pkg/metrics"
	"github.com/zxh326/kite/pkg/middleware"
	"github.com/zxh326/kite/pkg/model"
	"github.com/zxh326/kite/pkg/proxy"
	"github.com/zxh326/kite/pkg/rbac"
	"github.com/zxh326/kite/pkg/resources"
	"github.com/zxh326/kite/pkg/search"
	"github.com/zxh326/kite/pkg/settings"
	"github.com/zxh326/kite/pkg/system"
	"github.com/zxh326/kite/pkg/templates"
	"github.com/zxh326/kite/pkg/terminal"
	"github.com/zxh326/kite/pkg/users"
	"github.com/zxh326/kite/pkg/version"
	"github.com/zxh326/kite/pkg/workflowadapter"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

func setupAPIRouter(r *gin.RouterGroup, cm *cluster.ClusterManager, buildfleet *buildfleetadapter.Deps, credDeps *credentialsadapter.Deps) {
	authHandler := auth.NewAuthHandler()
	helmChartsHandler := helm.NewHelmChartHandler()

	registerBaseRoutes(r)
	r.GET("/api/v1/bootstrap", authHandler.Bootstrap)
	r.GET("/api/v1/cluster-agent/connect", cm.ConnectClusterAgent)
	r.POST("/api/v1/cluster-agent/register", cm.RegisterClusterAgent)
	r.GET("/api/v1/cluster-agent/manifest", cm.GetClusterAgentManifest)
	registerAuthRoutes(r, authHandler)
	registerUserRoutes(r, authHandler)
	registerAdminRoutes(r, authHandler, cm, helmChartsHandler)
	registerProtectedRoutes(r, authHandler, cm, helmChartsHandler, buildfleet, credDeps)
}

func registerBaseRoutes(r *gin.RouterGroup) {
	r.GET("/metrics", gin.WrapH(promhttp.HandlerFor(promclient.Gatherers{
		promclient.DefaultGatherer,
		ctrlmetrics.Registry,
	}, promhttp.HandlerOpts{})))
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.GET("/api/v1/version", version.GetVersion)
}

func registerAuthRoutes(r *gin.RouterGroup, authHandler *auth.AuthHandler) {
	authGroup := r.Group("/api/auth")
	authGroup.POST("/setup/create_super_user", authHandler.CreateSuperUser)
	authGroup.POST("/login/password", authHandler.PasswordLogin)
	authGroup.POST("/login/ldap", authHandler.LDAPLogin)
	authGroup.POST("/passkey/login/begin", authHandler.PasskeyLoginBegin)
	authGroup.POST("/passkey/login/finish", authHandler.PasskeyLoginFinish)
	authGroup.GET("/login", authHandler.Login)
	authGroup.GET("/callback", authHandler.Callback)
	authGroup.POST("/logout", authHandler.Logout)
	authGroup.POST("/refresh", authHandler.RefreshToken)
	authGroup.GET("/user", authHandler.RequireAuth(), authHandler.GetUser)
}

func registerUserRoutes(r *gin.RouterGroup, authHandler *auth.AuthHandler) {
	userGroup := r.Group("/api/users")
	userGroup.PUT("/me", authHandler.RequireAuth(), users.UpdateCurrentUser)
	userGroup.POST("/me/password", authHandler.RequireAuth(), users.ChangeCurrentUserPassword)
	userGroup.POST("/me/mfa/setup", authHandler.RequireAuth(), users.SetupCurrentUserMFA)
	userGroup.POST("/me/mfa/enable", authHandler.RequireAuth(), users.EnableCurrentUserMFA)
	userGroup.POST("/me/mfa/disable", authHandler.RequireAuth(), users.DisableCurrentUserMFA)
	userGroup.GET("/me/passkeys", authHandler.RequireAuth(), users.ListCurrentUserPasskeys)
	userGroup.POST("/me/passkeys/begin", authHandler.RequireAuth(), users.BeginCurrentUserPasskeyRegistration)
	userGroup.POST("/me/passkeys/finish", authHandler.RequireAuth(), users.FinishCurrentUserPasskeyRegistration)
	userGroup.DELETE("/me/passkeys/:id", authHandler.RequireAuth(), users.DeleteCurrentUserPasskey)
	userGroup.POST("/sidebar_preference", authHandler.RequireAuth(), users.UpdateSidebarPreference)
}

func registerAdminRoutes(r *gin.RouterGroup, authHandler *auth.AuthHandler, cm *cluster.ClusterManager, helmChartsHandler *helm.HelmChartHandler) {
	adminAPI := r.Group("/api/v1/admin")
	adminAPI.Use(authHandler.RequireAuth(), authHandler.RequireAdmin())

	adminAPI.GET("/audit-logs", audit.ListAuditLogs)

	oauthProviderAPI := adminAPI.Group("/oauth-providers")
	oauthProviderAPI.GET("/", authHandler.ListOAuthProviders)
	oauthProviderAPI.POST("/", authHandler.CreateOAuthProvider)
	oauthProviderAPI.GET("/:id", authHandler.GetOAuthProvider)
	oauthProviderAPI.PUT("/:id", authHandler.UpdateOAuthProvider)
	oauthProviderAPI.DELETE("/:id", authHandler.DeleteOAuthProvider)

	ldapSettingAPI := adminAPI.Group("/ldap-setting")
	ldapSettingAPI.GET("/", authHandler.GetLDAPSetting)
	ldapSettingAPI.PUT("/", authHandler.UpdateLDAPSetting)

	clusterAPI := adminAPI.Group("/clusters")
	clusterAPI.GET("/", cm.GetClusterList)
	clusterAPI.POST("/", cm.CreateCluster)
	clusterAPI.POST("/import", cm.ImportClustersFromKubeconfig)
	clusterAPI.PUT("/:id", cm.UpdateCluster)
	clusterAPI.DELETE("/:id", cm.DeleteCluster)

	// kcross 集成（设置/发现/绑定，全部在 pkg/kcross 内实现）
	kcrossAPI := adminAPI.Group("/kcross")
	kcross.RegisterRoutes(kcrossAPI)

	rbacAPI := adminAPI.Group("/roles")
	rbacAPI.GET("/", rbac.ListRoles)
	rbacAPI.POST("/", rbac.CreateRole)
	rbacAPI.GET("/:id", rbac.GetRole)
	rbacAPI.PUT("/:id", rbac.UpdateRole)
	rbacAPI.DELETE("/:id", rbac.DeleteRole)
	rbacAPI.POST("/:id/assign", rbac.AssignRole)
	rbacAPI.DELETE("/:id/assign", rbac.UnassignRole)

	userAPI := adminAPI.Group("/users")
	userAPI.GET("/", users.ListUsers)
	userAPI.POST("/", users.CreatePasswordUser)
	userAPI.PUT("/:id", users.UpdateUser)
	userAPI.DELETE("/:id", users.DeleteUser)
	userAPI.POST("/:id/reset_password", users.ResetPassword)
	userAPI.POST("/:id/enable", users.SetUserEnabled)
	adminAPI.POST("/sidebar_preference/global", users.UpdateGlobalSidebarPreference)
	adminAPI.DELETE("/sidebar_preference/global", users.ClearGlobalSidebarPreference)

	apiKeyAPI := adminAPI.Group("/apikeys")
	apiKeyAPI.GET("/", apikeys.ListAPIKeys)
	apiKeyAPI.POST("/", apikeys.CreateAPIKey)
	apiKeyAPI.DELETE("/:id", apikeys.DeleteAPIKey)

	generalSettingAPI := adminAPI.Group("/general-setting")
	generalSettingAPI.GET("/", settings.HandleGetGeneralSetting)
	generalSettingAPI.PUT("/", settings.HandleUpdateGeneralSetting)

	templateAPI := adminAPI.Group("/templates")
	templateAPI.POST("/", templates.CreateTemplate)
	templateAPI.PUT("/:id", templates.UpdateTemplate)
	templateAPI.DELETE("/:id", templates.DeleteTemplate)

	helmChartsHandler.RegisterAdminRoutes(adminAPI)
}

func registerProtectedRoutes(r *gin.RouterGroup, authHandler *auth.AuthHandler, cm *cluster.ClusterManager, helmChartsHandler *helm.HelmChartHandler, buildfleet *buildfleetadapter.Deps, credDeps *credentialsadapter.Deps) {
	api := r.Group("/api/v1")
	api.GET("/clusters", authHandler.RequireAuth(), cm.GetClusters)
	defaultAPI := api.Group("")
	defaultAPI.Use(authHandler.RequireAuth(), middleware.ClusterMiddleware(cm))

	appDeps := applicationadapter.NewDeps(cm)
	if err := applicationadapter.Migrate(model.DB); err != nil {
		panic("failed to migrate application module: " + err.Error())
	}
	if err := credentialsadapter.Migrate(model.DB); err != nil {
		panic("failed to migrate credentials module: " + err.Error())
	}
	// A typed-nil *buildfleetadapter.Deps would arrive as a non-nil
	// interface: convert explicitly so BuildImage sees "not configured".
	var fleetCap workflowadapter.FleetBuilder
	if buildfleet != nil {
		fleetCap = buildfleet
	}
	// appDeps backs both the application module API and the workflow
	// adapter's DeployApp facade (same Deps wiring, same audit attribution).
	wfDeps := workflowadapter.NewDeps(cm, fleetCap, credDeps, appDeps)
	if err := workflowadapter.Migrate(model.DB); err != nil {
		panic("failed to migrate workflow module: " + err.Error())
	}
	// Resume runs interrupted by a previous process (design §6 重启恢复);
	// must run after Migrate so the module tables exist.
	wfModule := workflow.New(wfDeps)
	if err := wfModule.Start(); err != nil {
		panic("failed to start workflow module: " + err.Error())
	}
	registerClusterProtectedRoutes(defaultAPI, helmChartsHandler, appDeps, buildfleet, wfModule, credDeps)

	clusterAPI := api.Group("/_clusters/:cluster")
	clusterAPI.Use(authHandler.RequireAuth(), middleware.ClusterMiddleware(cm))
	registerClusterProtectedRoutes(clusterAPI, helmChartsHandler, appDeps, buildfleet, wfModule, credDeps)
}

func registerClusterProtectedRoutes(api *gin.RouterGroup, helmChartsHandler *helm.HelmChartHandler, appDeps *applicationadapter.Deps, buildfleet *buildfleetadapter.Deps, wfModule *workflow.Module, credDeps *credentialsadapter.Deps) {
	api.GET("/overview", system.GetOverview)

	metricsHandler := metrics.NewHandler()
	api.GET("/prometheus/resource-usage-history", metricsHandler.GetResourceUsageHistory)
	api.GET("/prometheus/pods/:namespace/:podName/metrics", metricsHandler.GetPodMetrics)

	logsHandler := resources.NewLogsHandler()
	api.GET("/logs/:namespace/:podName/ws", logsHandler.HandleLogsWebSocket)

	terminalHandler := terminal.NewTerminalHandler()
	api.GET("/terminal/:namespace/:podName/ws", terminalHandler.HandleTerminalWebSocket)

	nodeTerminalHandler := terminal.NewNodeTerminalHandler()
	api.GET("/node-terminal/:nodeName/ws", nodeTerminalHandler.HandleNodeTerminalWebSocket)

	kubectlTerminalHandler := terminal.NewKubectlTerminalHandler()
	api.GET("/kubectl-terminal/ws", kubectlTerminalHandler.HandleKubectlTerminalWebSocket)

	searchHandler := search.NewSearchHandler(resources.SearchFuncs())
	api.GET("/search", searchHandler.GlobalSearch)

	resourceApplyHandler := resources.NewResourceApplyHandler()
	api.POST("/resources/apply", resourceApplyHandler.ApplyResource)

	api.GET("/image/tags", images.GetImageTags)
	api.GET("/templates", templates.ListTemplates)

	helmChartsHandler.RegisterRoutes(api)

	proxyHandler := proxy.NewProxyHandler()
	proxyHandler.RegisterRoutes(api)

	api.POST("/ai/chat", ai.HandleChat)
	api.POST("/ai/continue", ai.HandleContinue)

	application.RegisterRoutes(api, appDeps)
	// Mounted before the RBAC middleware below: buildfleet URLs are
	// non-resource shapes and authorize themselves against "builds".
	if buildfleet != nil {
		buildfleet.RegisterRoutes(api.Group("/buildfleet"))
	}
	// Same placement: workflow URLs are non-resource shapes and authorize
	// themselves against the "workflows" pseudo-resource (design §8).
	wfModule.RegisterRoutes(api)
	// Same placement: credentials URLs are non-resource shapes and
	// authorize themselves against the "credentials" pseudo-resource.
	credDeps.RegisterRoutes(api)

	api.Use(middleware.RBACMiddleware())
	resources.RegisterRoutes(api)
}
