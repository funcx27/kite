package users

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/zxh326/kite/pkg/model"
	"github.com/zxh326/kite/pkg/rbac"
)

// ListUserDirectory serves GET /api/v1/users/directory, the assignee-picker
// source for the workflow editor. It self-authorizes (mounted outside the
// RBAC middleware): admins and wildcard workflows-get holders may enumerate
// usernames, anyone else gets 403 so the endpoint cannot become a user-
// enumeration oracle.
func ListUserDirectory(c *gin.Context) {
	user := c.MustGet("user").(model.User)
	if !rbac.UserHasRole(user, model.DefaultAdminRole.Name) &&
		!rbac.CanAccess(user, "workflows", "get", "", "") {
		c.JSON(http.StatusForbidden, gin.H{"error": "not allowed to list the user directory"})
		return
	}
	entries, err := model.ListUserDirectory()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list users"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": entries})
}
