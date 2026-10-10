package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/funcx27/kite/modules/application/internal/render"
	"github.com/funcx27/kite/modules/application/internal/runtime"
	"github.com/funcx27/kite/modules/application/internal/store"
)

// ErrConfigModified reports that the config changed since the caller last
// read it (optimistic lock mismatch or a concurrent winner).
var ErrConfigModified = errors.New("config has been modified by someone else, refresh and retry")

// ErrWorkloadTypeChanged reports a workload type switch on a config that is
// anchored to live cluster resources; the escape hatch is offline-first.
var ErrWorkloadTypeChanged = errors.New("workload type cannot be changed while the config is deployed: offline the app first, then edit and republish")

// workloadKindOf maps the wizard workload type to its workload kind.
func workloadKindOf(workloadType string) string {
	if workloadType == render.WorkloadStatefulSet {
		return "StatefulSet"
	}
	return "Deployment"
}

// configResponse is the API view of a per-cluster config. Spec carries the
// form view extracted from Manifests (design §7 GET).
type configResponse struct {
	ID              uint            `json:"id"`
	AppID           uint            `json:"appId"`
	Cluster         string          `json:"cluster"`
	Namespace       string          `json:"namespace"`
	Manifests       json.RawMessage `json:"manifests"`
	Spec            json.RawMessage `json:"spec,omitempty"`
	LastApplied     json.RawMessage `json:"lastApplied"`
	CredentialRef   string          `json:"credentialRef"`
	StatusSummary   string          `json:"statusSummary"`
	StatusCheckedAt *time.Time      `json:"statusCheckedAt"`
	LastDeployedAt  *time.Time      `json:"lastDeployedAt"`
	LastSyncedAt    *time.Time      `json:"lastSyncedAt"`
	LatestRev       int             `json:"latestRev"`
}

func toConfigResponse(cfg *store.AppConfig, latestRev int) configResponse {
	return configResponse{
		ID:        cfg.ID,
		AppID:     cfg.AppID,
		Cluster:   cfg.Cluster,
		Namespace: cfg.Namespace,
		// Secret data/stringData values are masked for display (design §9);
		// saves restore stored values for round-tripped "***" markers.
		Manifests:       json.RawMessage(redactJSON(cfg.Manifests)),
		CredentialRef:   cfg.CredentialRef,
		LastApplied:     json.RawMessage(redactJSON(cfg.LastApplied)),
		StatusSummary:   cfg.StatusSummary,
		StatusCheckedAt: cfg.StatusCheckedAt,
		LastDeployedAt:  cfg.LastDeployedAt,
		LastSyncedAt:    cfg.LastSyncedAt,
		LatestRev:       latestRev,
	}
}

type updateConfigRequest struct {
	Spec        json.RawMessage `json:"spec"`
	Manifests   json.RawMessage `json:"manifests"`
	ExpectedRev *int            `json:"expectedRev"`
}

// renderAppSpec renders the app's stored draft spec into manifests.
func renderAppSpec(app *store.App) ([]unstructured.Unstructured, error) {
	if len(app.Spec) == 0 {
		return nil, errors.New("application has no spec to render")
	}
	var spec render.AppSpec
	if err := json.Unmarshal(app.Spec, &spec); err != nil {
		return nil, fmt.Errorf("stored spec is invalid: %w", err)
	}
	return render.Render(app.Name, spec, app.Namespace)
}

func (h *Handler) GetConfig(c *gin.Context) {
	app := h.findAppOr404(c)
	if app == nil {
		return
	}
	cfg, err := h.findConfig(app.ID, c.Param("cfgCluster"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		h.fail(c, http.StatusNotFound, nil, "config not found for cluster "+c.Param("cfgCluster"))
		return
	}
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if !h.authorize(c, "get", cfg.Cluster, cfg.Namespace) {
		return
	}
	latestRev, err := h.latestRev(cfg.ID)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	resp := toConfigResponse(cfg, latestRev)
	docs, err := store.DecodeManifests(cfg.Manifests)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	specJSON, err := json.Marshal(render.ExtractFields(app.Name, docs))
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	// The form view masks Secret file contents like the manifests (design §9:
	// Secret 只可查看文件名列表); saves round-trip the "***" markers and
	// restoreMaskedSecretValues substitutes the stored values.
	resp.Spec = json.RawMessage(redactJSON(specJSON))
	c.JSON(http.StatusOK, resp)
}

// validateUpdateConfigReq parses and validates one update request's payload
// (spec or manifests) into spec, writing the 400 response itself; false means
// the caller must stop.
func (h *Handler) validateUpdateConfigReq(c *gin.Context, appName string, req updateConfigRequest, spec *render.AppSpec) bool {
	if req.ExpectedRev == nil {
		h.fail(c, http.StatusBadRequest, nil, "expectedRev is required")
		return false
	}
	switch {
	case req.Spec != nil && req.Manifests != nil:
		h.fail(c, http.StatusBadRequest, nil, "provide either spec or manifests, not both")
		return false
	case req.Spec != nil:
		if !json.Valid(req.Spec) {
			h.fail(c, http.StatusBadRequest, nil, "spec must be valid JSON")
			return false
		}
		if err := json.Unmarshal(req.Spec, spec); err != nil {
			h.fail(c, http.StatusBadRequest, nil, "spec must be a valid AppSpec JSON object: "+err.Error())
			return false
		}
		spec.Normalize(appName)
		if err := spec.Validate(); err != nil {
			h.fail(c, http.StatusBadRequest, nil, "invalid spec: "+err.Error())
			return false
		}
	case req.Manifests == nil:
		h.fail(c, http.StatusBadRequest, nil, "manifests or spec is required")
		return false
	case !isManifestList(req.Manifests):
		h.fail(c, http.StatusBadRequest, nil, "manifests must be a JSON array of documents")
		return false
	}
	return true
}

// UpdateConfig replaces the config under an optimistic lock: expectedRev must
// equal the config's newest revision number, otherwise the config is reported
// as modified (409). The body carries either {spec} — injected onto the
// existing manifests, preserving uncovered fields (Source=wizard) — or
// {manifests} as a full replacement (Source=yaml). The lock re-check, the
// manifests update and the snapshot run in one transaction, so concurrent
// saves can neither lose an update nor leave a config without its snapshot.
func (h *Handler) UpdateConfig(c *gin.Context) {
	app := h.findAppOr404(c)
	if app == nil {
		return
	}
	cfg, err := h.findConfig(app.ID, c.Param("cfgCluster"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		h.fail(c, http.StatusNotFound, nil, "config not found for cluster "+c.Param("cfgCluster"))
		return
	}
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	if !h.authorize(c, "update", cfg.Cluster, cfg.Namespace) {
		return
	}
	var req updateConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	var spec render.AppSpec
	if !h.validateUpdateConfigReq(c, app.Name, req, &spec) {
		return
	}
	var incoming []unstructured.Unstructured
	if req.Manifests != nil {
		var err error
		incoming, err = store.DecodeManifests(req.Manifests)
		if err != nil {
			h.fail(c, http.StatusBadRequest, err, "")
			return
		}
	}
	var (
		updated  store.AppConfig
		previous string
		newRev   int
	)
	source := store.RevSourceYAML
	if req.Spec != nil {
		source = store.RevSourceWizard
	}
	txErr := h.store.DB().Transaction(func(tx *gorm.DB) error {
		cur, err := lockConfig(tx, cfg.ID)
		if err != nil {
			return err
		}
		var maxRev int
		if err := tx.Model(&store.ConfigRevision{}).
			Where("app_config_id = ?", cur.ID).
			Select("COALESCE(MAX(rev), 0)").
			Scan(&maxRev).Error; err != nil {
			return err
		}
		if *req.ExpectedRev != maxRev {
			return ErrConfigModified
		}
		previous = string(cur.Manifests)
		var next []unstructured.Unstructured
		if req.Spec != nil {
			docs, err := store.DecodeManifests(cur.Manifests)
			if err != nil {
				return err
			}
			// The workload kind is immutable while the config is anchored to
			// cluster resources: switching Deployment↔StatefulSet would leave
			// the old controller running as unmanaged cluster-side drift.
			// Offline clears the anchor and reopens the type for change.
			if anchor, err := store.DecodeManifests(cur.LastApplied); err != nil {
				return err
			} else if len(anchor) > 0 {
				if wl := runtime.WorkloadOf(app.Name, docs); wl != nil &&
					workloadKindOf(spec.Workload.Type) != wl.GetKind() {
					return ErrWorkloadTypeChanged
				}
			}
			next, err = render.InjectFields(app.Name, cur.Namespace, docs, spec)
			if err != nil {
				return err
			}
			// Keep the managed pull secret reference in lockstep with the
			// config's credential so it never becomes drift.
			render.InjectPullSecret(next, pullSecretName(cur.CredentialRef))
		} else {
			next = incoming
		}
		prevDocs, err := store.DecodeManifests(cur.Manifests)
		if err != nil {
			return err
		}
		restoreMaskedSecretValues(next, prevDocs)
		data, err := store.EncodeManifests(next)
		if err != nil {
			return err
		}
		cur.Manifests = store.SecretJSON(data)
		if err := tx.Model(&store.AppConfig{}).Where("id = ?", cur.ID).
			Update("manifests", cur.Manifests).Error; err != nil {
			return err
		}
		newRev, err = store.AppendRevisionTx(tx, cur.ID, source, cur.Manifests, h.deps.OperatorID(c), 0)
		if err != nil {
			return err
		}
		updated = *cur
		return nil
	})
	switch {
	case errors.Is(txErr, ErrConfigModified), isConcurrentWriteError(txErr):
		h.fail(c, http.StatusConflict, nil, ErrConfigModified.Error())
		return
	case errors.Is(txErr, ErrWorkloadTypeChanged):
		h.fail(c, http.StatusBadRequest, nil, ErrWorkloadTypeChanged.Error())
		return
	case txErr != nil:
		h.fail(c, http.StatusInternalServerError, txErr, "")
		return
	}
	h.recordAudit(h.requestIdentity(c), store.HistoryEntry{
		Cluster: cfg.Cluster, ResourceType: authzResource, ResourceName: app.Name,
		Namespace: cfg.Namespace, Operation: "update",
		YAML:         redacted(req.Manifests),
		PreviousYAML: redacted([]byte(previous)),
	})
	c.JSON(http.StatusOK, toConfigResponse(&updated, newRev))
}

// restoreMaskedSecretValues substitutes the previously stored value for keys
// a client round-tripped as the redaction marker "***", so serving masked
// manifests (design §9) never overwrites real secret content on save. A
// marker without a stored counterpart is kept as-is.
func restoreMaskedSecretValues(next, previous []unstructured.Unstructured) {
	for i := range next {
		n := &next[i]
		if n.GetKind() != "Secret" {
			continue
		}
		var prev map[string]interface{}
		for j := range previous {
			if store.DocKeyOf(&previous[j]) == store.DocKeyOf(n) {
				prev = previous[j].Object
				break
			}
		}
		for _, field := range []string{"data", "stringData"} {
			nm, ok := store.AsMap(n.Object[field])
			if !ok {
				continue
			}
			var pm map[string]interface{}
			if prev != nil {
				pm, _ = store.AsMap(prev[field])
			}
			for k, v := range nm {
				if s, _ := v.(string); s == redactMarker {
					if pv, ok := pm[k]; ok {
						nm[k] = pv
					}
				}
			}
		}
	}
}

// lockConfig re-reads the config row inside the transaction, taking a row
// lock on dialects with SELECT ... FOR UPDATE so concurrent saves serialize
// before the revision check; sqlite drops the clause (single writer) and
// falls back to the per-config unique index as the race backstop.
func lockConfig(tx *gorm.DB, id uint) (*store.AppConfig, error) {
	q := tx.Where("id = ?", id)
	if tx.Name() != "sqlite" {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var cfg store.AppConfig
	if err := q.First(&cfg).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

// isManifestList checks that manifests are a JSON array of documents (design
// §3: multi-doc []unstructured). Rejecting other JSON shapes also keeps the
// audit redaction walk sound, which only descends into array/object trees.
func isManifestList(doc []byte) bool {
	var list []json.RawMessage
	return json.Unmarshal(doc, &list) == nil
}

// isConcurrentWriteError reports transaction failures caused by a concurrent
// writer on the same config: a unique-index violation (two saves raced past
// the revision check) or a database-level write contention error (sqlite
// BUSY, which replaces row locking). Both mean the config changed under the
// caller, so the transaction rolled back and the optimistic-lock 409 with a
// refresh-and-retry is the correct answer.
func isConcurrentWriteError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unique constraint failed"),
		strings.Contains(msg, "duplicate entry"),
		strings.Contains(msg, "duplicate key value"),
		strings.Contains(msg, "database is locked"),
		strings.Contains(msg, "sqlite_busy"):
		return true
	}
	return false
}
