package middleware

import "strings"

// workflowHookPathPrefix is the workflow module's public webhook route; its
// path segment carries the webhook token, a credential, so the tail is
// redacted rather than logged (never log secrets). Literal here: pkg/ must
// not import the module (modules/README.md contract §2).
const workflowHookPathPrefix = "/api/workflow-hooks/"

// redactSensitivePath masks credential-bearing path tails for the access log.
func redactSensitivePath(path string) string {
	if strings.HasPrefix(path, workflowHookPathPrefix) {
		return workflowHookPathPrefix + "redacted"
	}
	return path
}
