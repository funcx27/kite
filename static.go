package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/zxh326/kite/pkg/common"
	"github.com/zxh326/kite/pkg/middleware"
	"github.com/zxh326/kite/pkg/utils"
)

//go:embed static
var staticFiles embed.FS

// The portal SPA owns the single /portal prefix. The fallback must happen
// here, server-side: the console router has a :resource/:name catch-all, so
// portal routes like /portal/applications served as index.html would be
// rendered as cluster resource pages by the console entry.
func isPortalPath(path, base string) bool {
	if !strings.HasPrefix(path, base) {
		return false
	}
	rest := path[len(base):]
	return rest == "/portal" || strings.HasPrefix(rest, "/portal/")
}

func serveSPAHTML(c *gin.Context, name, base string) {
	content, err := staticFiles.ReadFile("static/" + name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read " + name})
		return
	}

	htmlContent := utils.InjectKiteBase(string(content), base)
	if common.EnableAnalytics {
		htmlContent = utils.InjectAnalytics(htmlContent)
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, htmlContent)
}

func setupStatic(r *gin.Engine) {
	base := common.Base
	if base != "" && base != "/" {
		r.GET("/", func(c *gin.Context) {
			c.Redirect(http.StatusFound, base+"/")
		})
	}

	assetsFS, err := fs.Sub(staticFiles, "static/assets")
	if err != nil {
		panic(err)
	}

	assetsGroup := r.Group(base + "/assets")
	assetsGroup.Use(middleware.StaticCache())
	assetsGroup.StaticFS("/", http.FS(assetsFS))

	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if len(path) >= len(base)+5 && path[len(base):len(base)+5] == "/api/" {
			c.JSON(http.StatusNotFound, gin.H{"error": "API endpoint not found"})
			return
		}

		if isPortalPath(path, base) {
			serveSPAHTML(c, "portal.html", base)
			return
		}
		serveSPAHTML(c, "index.html", base)
	})
}
