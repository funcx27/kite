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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/application/internal/store"
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

type mergeRequest struct {
	// Choices keys are scoped paths "<docKey>:<path>" (DocRootPath for
	// document-level collisions), values are ours|theirs (design §4).
	Choices map[string]string `json:"choices,omitempty"`
	// Rollbacks lists scoped paths of auto-adopted cluster changes reverted
	// to the baseline; they are persisted as OverriddenPaths.
	Rollbacks []string `json:"rollbacks,omitempty"`
}

// MergePreview computes the three-way merge without writing anything
// (design §7): merged result, auto-adoptions, collisions and per-document
// reports for the merge dialog.
func (h *Handler) MergePreview(c *gin.Context) {
	app, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	if !h.authorize(c, "get", cfg.Cluster, cfg.Namespace) {
		return
	}
	cli, err := h.deps.ResolveCluster(c.Request.Context(), cfg.Cluster)
	if err != nil {
		h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+cfg.Cluster)
		return
	}
	res, theirs, code, err := h.computeMerge(c, cli, app, cfg, nil)
	if err != nil {
		h.fail(c, code, err, "")
		return
	}
	h.writeMergePreview(c, res, len(syncops.DiffManifests(res.Merged, theirs)) > 0)
}

// writeMergePreview serves the merge result with Secret content masked
// (design §9): the standard redaction walk covers whole documents, and
// collision values that sit on a Secret doc's data/stringData path are
// masked as well. publishRequired tells the dialog up front whether
// confirming ends in a synced config (pure adoption) or a publish.
func (h *Handler) writeMergePreview(c *gin.Context, res *syncops.MergeResult, publishRequired bool) {
	masked := *res
	masked.Docs = make([]syncops.MergeDocReport, len(res.Docs))
	for i := range res.Docs {
		masked.Docs[i] = res.Docs[i]
		masked.Docs[i].Collisions = make([]syncops.MergeCollision, len(res.Docs[i].Collisions))
		for j := range res.Docs[i].Collisions {
			masked.Docs[i].Collisions[j] = res.Docs[i].Collisions[j]
			if res.Docs[i].Kind == "Secret" && secretDataPath(res.Docs[i].Collisions[j].Path) {
				masked.Docs[i].Collisions[j].Base = redactMarker
				masked.Docs[i].Collisions[j].Ours = redactMarker
				masked.Docs[i].Collisions[j].Theirs = redactMarker
			}
		}
	}
	data, err := json.Marshal(struct {
		syncops.MergeResult
		PublishRequired bool `json:"publishRequired"`
	}{masked, publishRequired})
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	c.Data(http.StatusOK, "application/json", redactJSON(data))
}

// secretDataPath reports whether a document-relative path resolves into a
// Secret's data/stringData map (or the whole document).
func secretDataPath(path string) bool {
	if path == syncops.DocRootPath {
		return true
	}
	return strings.HasPrefix(path, "data.") || strings.HasPrefix(path, "stringData.") ||
		path == "data" || path == "stringData"
}

// MergeConfig applies the caller's collision choices and rollback list
// (design §4/§7): the merged set is written to Manifests with a Source=merge
// revision and the knowingly-overridden paths land in OverriddenPaths, ready
// for the conflict-gated publish. Unresolved collisions are rejected. A pure
// adoption (the merged set already matches the cluster) re-anchors in place
// and answers publishRequired=false — publishing could only ask for the
// no-op apply the deploy dialog refuses (design §6 改镜像即发布).
func (h *Handler) MergeConfig(c *gin.Context) {
	app, cfg, ok := h.findAppConfig(c)
	if !ok {
		return
	}
	if !h.authorize(c, "update", cfg.Cluster, cfg.Namespace) {
		return
	}
	cli, err := h.deps.ResolveCluster(c.Request.Context(), cfg.Cluster)
	if err != nil {
		h.fail(c, http.StatusNotFound, nil, "cluster not found or offline: "+cfg.Cluster)
		return
	}
	var req mergeRequest
	if err := bindOptionalJSON(c, &req); err != nil {
		h.fail(c, http.StatusBadRequest, err, "")
		return
	}
	resolutions, err := parseMergeResolutions(req)
	if err != nil {
		h.fail(c, http.StatusBadRequest, nil, err.Error())
		return
	}
	res, theirs, code, err := h.computeMerge(c, cli, app, cfg, resolutions)
	if err != nil {
		h.fail(c, code, err, "")
		return
	}
	var unresolved []string
	for _, rep := range res.Docs {
		key := store.DocKey(rep.Kind, rep.Namespace, rep.Name)
		for _, col := range rep.Collisions {
			if col.Chosen == "" {
				unresolved = append(unresolved, syncops.ScopedPath(key, col.Path))
			}
		}
	}
	if len(unresolved) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":      "merge has unresolved collisions, choose ours or theirs for each",
			"unresolved": unresolved,
		})
		return
	}

	data, err := store.EncodeManifests(res.Merged)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	publishRequired := len(syncops.DiffManifests(res.Merged, theirs)) > 0
	var overridden []string
	for _, rep := range res.Docs {
		key := store.DocKey(rep.Kind, rep.Namespace, rep.Name)
		for _, p := range rep.Overridden {
			overridden = append(overridden, syncops.ScopedPath(key, p))
		}
		// A collision resolved to ours is a knowing override too: the user
		// explicitly chose to publish the DB value over the cluster's, and
		// the conflict gate must let that publish through.
		for _, col := range rep.Collisions {
			if col.Chosen == syncops.ChooseOurs {
				overridden = append(overridden, syncops.ScopedPath(key, col.Path))
			}
		}
	}
	overriddenData, err := json.Marshal(overridden)
	if err != nil {
		h.fail(c, http.StatusInternalServerError, err, "")
		return
	}
	previous := string(cfg.Manifests)
	now := time.Now()
	newRev := 0
	txErr := h.store.DB().Transaction(func(tx *gorm.DB) error {
		cur, err := lockConfig(tx, cfg.ID)
		if err != nil {
			return err
		}
		updates := map[string]interface{}{
			"manifests":         store.SecretJSON(data),
			"overridden_paths":  store.JSONField(overriddenData),
			"status_summary":    store.StatusMerged,
			"status_checked_at": now,
		}
		if !publishRequired {
			// The merged set is already live: it becomes the anchor and the
			// config is synced without a publish.
			updates["last_applied"] = store.SecretJSON(data)
			updates["status_summary"] = store.StatusSynced
		}
		if err := tx.Model(&store.AppConfig{}).Where("id = ?", cur.ID).Updates(updates).Error; err != nil {
			return err
		}
		newRev, err = store.AppendRevisionTx(tx, cur.ID, store.RevSourceMerge, data, h.deps.OperatorID(c), 0)
		return err
	})
	if txErr != nil {
		h.fail(c, http.StatusInternalServerError, txErr, "")
		return
	}
	h.recordAudit(c, store.HistoryEntry{
		Cluster: cfg.Cluster, ResourceType: authzResource, ResourceName: app.Name,
		Namespace: cfg.Namespace, Operation: "update",
		YAML:         redacted(data),
		PreviousYAML: redacted(store.JSONField(previous)),
	})
	c.JSON(http.StatusOK, gin.H{
		"latestRev":       newRev,
		"overriddenPaths": overridden,
		"publishRequired": publishRequired,
	})
}

// parseMergeResolutions splits the scoped request keys into per-document
// resolutions and rejects unknown choice values.
func parseMergeResolutions(req mergeRequest) (syncops.MergeResolutions, error) {
	res := syncops.MergeResolutions{}
	for key, choice := range req.Choices {
		if choice != syncops.ChooseOurs && choice != syncops.ChooseTheirs {
			return nil, fmt.Errorf("invalid choice %q for %s (must be %q or %q)", choice, key, syncops.ChooseOurs, syncops.ChooseTheirs)
		}
		doc, path := syncops.SplitScopedPath(key)
		d := res[doc]
		if d.Choices == nil {
			d.Choices = map[string]string{}
		}
		d.Choices[path] = choice
		res[doc] = d
	}
	for _, key := range req.Rollbacks {
		doc, path := syncops.SplitScopedPath(key)
		d := res[doc]
		d.Rollbacks = append(d.Rollbacks, path)
		res[doc] = d
	}
	return res, nil
}

// computeMerge fetches the three merge inputs (anchor, manifests, live) and
// runs the pure three-way merge. It also returns the normalized live docs
// the merge ran against, so the caller can tell a pure adoption from a
// merged set still awaiting publish. Failures surface as (nil, nil, code, err).
func (h *Handler) computeMerge(c *gin.Context, cli client.Client, app *store.App, cfg *store.AppConfig, resolutions syncops.MergeResolutions) (*syncops.MergeResult, []unstructured.Unstructured, int, error) {
	manifests, err := store.DecodeManifests(cfg.Manifests)
	if err != nil {
		return nil, nil, http.StatusInternalServerError, err
	}
	anchor, err := store.DecodeManifests(cfg.LastApplied)
	if err != nil {
		return nil, nil, http.StatusInternalServerError, err
	}
	if len(anchor) == 0 {
		return nil, nil, http.StatusBadRequest, errors.New("config has no anchor (never deployed), nothing to merge")
	}
	templates := syncops.IdentityTemplates(manifests, anchor)
	live, err := syncops.FetchLiveDocs(c.Request.Context(), cli, templates)
	if err != nil {
		return nil, nil, http.StatusInternalServerError, err
	}
	// Theirs is normalized so server-managed fields (status, clusterIP,
	// last-applied annotations) are not adopted as cluster-side changes.
	theirs := syncops.NormalizeManifests(syncops.LiveList(live, templates))
	res, err := syncops.MergeManifests(anchor, manifests, theirs, resolutions)
	if err != nil {
		return nil, nil, http.StatusBadRequest, err
	}
	return res, theirs, http.StatusOK, nil
}
