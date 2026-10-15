package main

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/zxh326/kite/pkg/common"
	"github.com/zxh326/kite/pkg/utils"
)

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

// servePortalSPA serves portal.html for portal routes; false means the
// request is not a portal path and the console fallback should proceed.
func servePortalSPA(c *gin.Context, path, base string) bool {
	if !isPortalPath(path, base) {
		return false
	}
	content, err := staticFiles.ReadFile("static/portal.html")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read portal.html"})
		return true
	}
	htmlContent := utils.InjectKiteBase(string(content), base)
	if common.EnableAnalytics {
		htmlContent = utils.InjectAnalytics(htmlContent)
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, htmlContent)
	return true
}
