package buildfleetadapter

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	"github.com/funcx27/kite/modules/buildfleet/fleet"
	"github.com/funcx27/kite/modules/buildfleet/gitctx"
	"github.com/funcx27/kite/modules/buildfleet/runtime"
	"github.com/zxh326/kite/pkg/credentialsadapter"
	"github.com/zxh326/kite/pkg/model"
)

// gitRefsRequest is the POST /git-refs body: the repository to advertise,
// the credential-center ref its fetch authenticates with, and an optional
// provider pin for the probe fallback stage.
type gitRefsRequest struct {
	URL        string `json:"url"`
	Credential string `json:"credential"`
	Provider   string `json:"provider"`
}

// gitRefsResponse answers with the repository's branch/tag names and which
// stage produced them: "direct" (host ls-remote) or "probe" (buildkitd-side
// ls-remote harvested from a fleet solve).
type gitRefsResponse struct {
	Branches []string `json:"branches"`
	Tags     []string `json:"tags"`
	Source   string   `json:"source"`
}

const (
	gitRefsSourceDirect = "direct"
	gitRefsSourceProbe  = "probe"
	// gitRefsProbeTimeout bounds the probe fallback stage: a cold k8s
	// runtime (pod schedule + buildkitd readiness) plus the probe solve
	// can run tens of seconds, far beyond the direct path's budget.
	gitRefsProbeTimeout = 3 * time.Minute
)

// handleGitRefs lists a repository's branches and tags for the build
// editor's ref picker, in two stages: a host-side ls-remote first (fast,
// no build fleet involvement), and on its failure a probe through a build
// provider — buildkitd's own ls-remote advertisement harvested from the
// solve's git-source vertex, the vantage that serves the actual builds.
// Both stages failing is an explicit error; the client then degrades to
// free-text ref entry.
func (d *Deps) handleGitRefs(c *gin.Context) {
	var req gitRefsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request body: %v", err)})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	req.Credential = strings.TrimSpace(req.Credential)
	req.Provider = strings.TrimSpace(req.Provider)
	if req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url is required"})
		return
	}
	// The probe stage drives a full fleet solve — the same resource profile
	// as POST /builds and runtime-mode preview — so the endpoint carries
	// the create verb even when the direct path ends up answering.
	if !authorize(c, "create") {
		return
	}
	if req.Credential != "" && !canUseCredentials(c) {
		return
	}

	src := gitctx.Source{URL: req.URL}
	freq := fleet.Request{Git: &src}
	if req.Credential != "" {
		if d.creds == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("credential %q cannot be resolved: credential center is not wired", req.Credential)})
			return
		}
		if err := d.creds.InjectFleetCredentials(&freq, req.Credential, ""); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	// Stage 1 — host ls-remote (fast path; gitctx caps it at its own
	// ls-remote timeout).
	refs, directErr := gitctx.ListRefs(c.Request.Context(), src)
	if directErr == nil {
		c.JSON(http.StatusOK, gitRefsResponse{Branches: refs.Branches, Tags: refs.Tags, Source: gitRefsSourceDirect})
		return
	}

	// Stage 2 — probe harvest through a build provider.
	provider := req.Provider
	if provider == "" {
		provider = d.pickProbeProvider()
	}
	if provider == "" {
		d.gitRefsFailed(c, directErr, fmt.Errorf("no k8s build provider available for the probe fallback"))
		return
	}
	probeSrc := src
	probeSrc.Mode = "runtime"
	probeReq := fleet.Request{Git: &probeSrc, Provider: provider, Creds: freq.Creds}
	probeCtx, cancel := context.WithTimeout(c.Request.Context(), gitRefsProbeTimeout)
	defer cancel()
	res, probeErr := d.app.Fleet.ProbeRefs(probeCtx, probeReq)
	if probeErr != nil {
		d.gitRefsFailed(c, directErr, fmt.Errorf("probe via provider %s: %w", provider, probeErr))
		return
	}
	c.JSON(http.StatusOK, gitRefsResponse{Branches: res.Branches, Tags: res.Tags, Source: gitRefsSourceProbe})
}

// gitRefsFailed answers the two-stage listing failure with both causes:
// source errors may embed the client-supplied URL (or its basic-auth
// userinfo), so the combined message is redacted like preview errors.
func (d *Deps) gitRefsFailed(c *gin.Context, directErr, probeErr error) {
	msg := credentialsadapter.RedactCredentials(
		fmt.Sprintf("listing refs failed — direct ls-remote: %v; probe fallback: %v", directErr, probeErr))
	c.JSON(http.StatusBadGateway, gin.H{"error": msg})
}

// pickProbeProvider chooses the provider the ref-listing probe runs on when
// the request did not pin one. The advertisement depends only on the git
// server, so any enabled k8s provider answers equally — prefer one holding
// a warm runtime (no cold-start wait). Docker providers are skipped: their
// buildkitd shares the kite host's egress, which stage 1 just failed on.
func (d *Deps) pickProbeProvider() string {
	warm := map[string]bool{}
	for _, s := range d.app.Fleet.Stats() {
		if s.State == runtime.Warm {
			warm[s.Owner] = true
		}
	}
	var names []string
	for _, p := range d.cfg.Providers {
		if p.Type == "k8s" {
			names = append(names, p.Name)
		}
	}
	rows, err := model.ListBuildProviders()
	if err != nil {
		klog.Warningf("git-refs probe fallback: list managed providers: %v", err)
	} else {
		for _, row := range rows {
			if row.Enabled && effectiveType(row) == "k8s" {
				names = append(names, row.Name)
			}
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if warm[n] {
			return n
		}
	}
	if len(names) > 0 {
		return names[0]
	}
	return ""
}
