package middleware

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/zxh326/kite/pkg/model"
)

var unlogPath = []string{
	"/healthz",
	"/assets/",
	"/favicon.ico",
	"/metrics",
}

// workflowHookPathPrefix is the workflow module's public webhook route; its
// path segment carries the webhook token, a credential, so the tail is
// redacted rather than logged (never log secrets). Literal here: pkg/ must
// not import the module (modules/README.md contract §2).
const workflowHookPathPrefix = "/api/workflow-hooks/"

func Logger() gin.HandlerFunc {
	return gin.LoggerWithConfig(gin.LoggerConfig{
		Formatter: func(param gin.LogFormatterParams) string {
			// Skip logging for health check and static asset paths
			for _, path := range unlogPath {
				if param.Path == path || strings.HasPrefix(param.Path, path) {
					return ""
				}
			}
			if strings.HasPrefix(param.Path, workflowHookPathPrefix) {
				param.Path = workflowHookPathPrefix + "redacted"
			}
			name := "-"
			if v, ok := param.Keys["user"]; ok {
				if user, ok := v.(model.User); ok {
					name = user.Key()
				}
			}

			cluster := "-"
			if clusterName, exists := param.Keys[ClusterNameKey]; exists {
				cluster = clusterName.(string)
			}

			return fmt.Sprintf("%s - %s \"%s %s\" %d %s %s %s\n",
				param.ClientIP,
				param.TimeStamp.Format("2006-01-02 15:04:05"),
				param.Method,
				param.Path,
				param.StatusCode,
				param.Latency,
				cluster,
				name,
			)
		},
	})
}
