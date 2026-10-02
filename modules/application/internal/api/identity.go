package api

import "github.com/gin-gonic/gin"

// identity is the acting principal of a publish pipeline run: the audit
// operator plus the permission check. HTTP requests bind it from the gin
// context; the exported deploy facade binds it from a user id, since workflow
// runs deploy outside any request (workflow design §5.2/§7).
type identity struct {
	operatorID uint
	authorize  func(resource, verb, cluster, namespace string) bool
}

// requestIdentity binds the identity to the request's authenticated user.
func (h *Handler) requestIdentity(c *gin.Context) identity {
	return identity{
		operatorID: h.deps.OperatorID(c),
		authorize: func(resource, verb, cluster, namespace string) bool {
			return h.deps.Authorize(c, resource, verb, cluster, namespace)
		},
	}
}
