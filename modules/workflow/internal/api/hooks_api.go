package api

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/funcx27/kite/modules/workflow/internal/engine"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// hookPath is the public webhook route mounted by RegisterPublicRoutes: the
// token is carried in the path, so the route sits outside any session or
// RBAC middleware (design §7).
const hookPath = "/api/workflow-hooks/:token"

// hookMaxBodyBytes bounds the accepted payload size.
const hookMaxBodyBytes = 1 << 20

// RegisterPublicRoutes mounts the webhook receiver on the host's public
// router group (design §7): no session and no RBAC — the 128-bit token in
// the path is the credential, matched constant-time; unknown tokens answer
// 404 like any other miss. Created runs execute with the last editor of the
// trigger config as RBAC identity (design §7 运行身份).
func RegisterPublicRoutes(r *gin.RouterGroup, deps Deps, st *store.Store, eng *engine.Engine) {
	h := &Handler{deps: deps, store: st, engine: eng}
	r.POST(hookPath, h.WebhookTrigger)
}

// WebhookTrigger admits a run from a git host push event. Non-push events
// (PRs, issues), tag pushes, branch deletions and unrecognized payloads are
// ignored with 200 (design §7 v1 口径).
func (h *Handler) WebhookTrigger(c *gin.Context) {
	wf, ok := h.workflowByToken(c, c.Param("token"))
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, hookMaxBodyBytes))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unreadable payload"})
		return
	}
	push, ok := adaptPush(c.Request.Header, body)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"ignored": true})
		return
	}
	res, err := h.engine.TriggerRun(wf, push.params(), store.TriggerWebhook, push.triggerRef(), 0)
	if err != nil {
		// Keep the public surface generic: error details would disclose
		// workflow-definition specifics.
		switch {
		case errors.Is(err, engine.ErrTriggerDisabled), errors.Is(err, engine.ErrNoNodes):
			c.JSON(http.StatusConflict, gin.H{"error": "workflow does not accept webhook triggers"})
		case errors.Is(err, engine.ErrInvalidTrigger):
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "payload does not satisfy the workflow parameters"})
		default:
			slog.Error("workflow: webhook trigger failed", "workflow", wf.Name, "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		}
		return
	}
	detail, err := json.Marshal(map[string]any{"run": res.RunID, "trigger": "webhook", "ref": push.triggerRef()})
	if err != nil {
		detail = nil
	}
	h.deps.RecordHistory(store.HistoryEntry{
		ResourceType: authzResource,
		ResourceName: fmt.Sprintf("%s#%d", wf.Name, res.RunNumber),
		Operation:    "trigger-webhook",
		YAML:         string(detail),
		Success:      true,
		OperatorID:   res.TriggeredBy,
	})
	c.JSON(http.StatusOK, gin.H{"id": res.RunID, "runNumber": res.RunNumber})
}

// workflowByToken resolves the path token to its workflow. Tokens decrypt on
// scan, so matching walks the table comparing constant-time; unknown tokens
// answer 404 without disclosing workflow existence. Requests whose token
// cannot be a generated one are rejected before any database work — this
// unauthenticated endpoint must not amplify into N decryptions per junk
// request. Writes the response itself when ok=false.
func (h *Handler) workflowByToken(c *gin.Context, token string) (*store.Workflow, bool) {
	if len(token) != webhookTokenHexLen {
		c.Status(http.StatusNotFound)
		return nil, false
	}
	var workflows []store.Workflow
	if err := h.store.DB().
		Select("id", "name", "current_rev", "webhook_token").
		Find(&workflows).Error; err != nil {
		slog.Error("workflow: webhook token lookup failed", "err", err)
		c.Status(http.StatusInternalServerError)
		return nil, false
	}
	for i := range workflows {
		stored := string(workflows[i].WebhookToken)
		if stored == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(stored)) == 1 {
			return &workflows[i], true
		}
	}
	c.Status(http.StatusNotFound)
	return nil, false
}

// push is what the adapter extracted from one push event (design §7).
type push struct {
	Ref    string // full ref, refs/heads/<branch>
	Branch string
	SHA    string
	Repo   string
}

// params maps the extraction onto the reserved parameter names; keys absent
// from the workflow's schema are ignored by parameter resolution, and empty
// extractions are dropped.
func (p push) params() map[string]any {
	m := map[string]any{}
	for k, v := range map[string]string{"branch": p.Branch, "ref": p.Ref, "sha": p.SHA, "repo": p.Repo} {
		if v != "" {
			m[k] = v
		}
	}
	return m
}

// triggerRef prefers the commit, falling back to the full ref.
func (p push) triggerRef() string {
	if p.SHA != "" {
		return p.SHA
	}
	return p.Ref
}

// pushPayload is the union of the github/gitea/gitlab push shapes the
// adapter reads (design §7).
type pushPayload struct {
	Ref         string `json:"ref"`
	After       string `json:"after"`
	CheckoutSHA string `json:"checkout_sha"`
	Deleted     bool   `json:"deleted"`
	ObjectKind  string `json:"object_kind"`
	HeadCommit  struct {
		ID string `json:"id"`
	} `json:"head_commit"`
	Repository struct {
		FullName string `json:"full_name"`
		Name     string `json:"name"`
	} `json:"repository"`
	Project struct {
		PathWithNamespace string `json:"path_with_namespace"`
		Name              string `json:"name"`
	} `json:"project"`
}

// hookSource identifies the git host from the vendor event headers
// (design §7: X-GitHub-Event 等特征头); source is empty without one.
func hookSource(h http.Header) (source, event string) {
	if v := h.Get("X-Github-Event"); v != "" {
		return "github", strings.ToLower(v)
	}
	if v := h.Get("X-Gitea-Event"); v != "" {
		return "gitea", strings.ToLower(v)
	}
	if v := h.Get("X-Gitlab-Event"); v != "" {
		return "gitlab", strings.ToLower(v)
	}
	return "", ""
}

// zeroSHA is the git null SHA; deletion pushes carry it in After.
const zeroSHA = "0000000000000000000000000000000000000000"

// pushEventAccepted judges the vendor event header (and, headerless, the
// body's own object_kind): only push events trigger, and an unrecognized
// payload is ignored (design §7 v1 口径) — headerless bodies must declare
// themselves a push via object_kind.
func pushEventAccepted(source, event, objectKind string) bool {
	switch source {
	case "github", "gitea":
		return event == "push"
	case "gitlab":
		return event == "push hook" || event == "push"
	default:
		return strings.EqualFold(objectKind, "push")
	}
}

// adaptPush extracts repo/ref/sha from a git host push payload. ok=false
// means the payload is not a branch push — non-push events, tag pushes,
// branch deletions, unparseable or unrecognized bodies — and v1 ignores it.
func adaptPush(header http.Header, body []byte) (push, bool) {
	source, event := hookSource(header)
	var raw pushPayload
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&raw); err != nil {
		return push{}, false
	}
	if !pushEventAccepted(source, event, raw.ObjectKind) {
		return push{}, false
	}
	branch, ok := strings.CutPrefix(raw.Ref, "refs/heads/")
	if !ok || branch == "" {
		return push{}, false // tags, refs/merge-requests/*, malformed refs
	}
	if raw.Deleted || raw.After == zeroSHA {
		return push{}, false
	}
	sha := raw.After
	if sha == "" {
		sha = raw.CheckoutSHA
	}
	if sha == "" {
		sha = raw.HeadCommit.ID
	}
	if sha == zeroSHA {
		sha = ""
	}
	return push{Ref: raw.Ref, Branch: branch, SHA: sha, Repo: pushRepo(raw)}, true
}

// pushRepo picks the most specific repository identifier each vendor offers.
func pushRepo(raw pushPayload) string {
	switch {
	case raw.Repository.FullName != "":
		return raw.Repository.FullName
	case raw.Project.PathWithNamespace != "":
		return raw.Project.PathWithNamespace
	case raw.Repository.Name != "":
		return raw.Repository.Name
	default:
		return raw.Project.Name
	}
}
