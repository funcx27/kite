package buildfleetadapter

import (
	"container/list"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"unicode"

	"github.com/gin-gonic/gin"

	"github.com/funcx27/kite/modules/buildfleet/fleet"
	"github.com/funcx27/kite/modules/buildfleet/gitctx"
	"github.com/zxh326/kite/pkg/credentialsadapter"
)

// dockerfilePreviewRequest is the POST /dockerfile-preview body: the git
// source (subdir included — it moves the context root exactly like a build
// node's git source), the context-relative dockerfile path to fetch, and
// the same git credential ref / fetch mode a build node would carry.
type dockerfilePreviewRequest struct {
	URL        string `json:"url"`
	Ref        string `json:"ref"`
	Subdir     string `json:"subdir"`
	Path       string `json:"path"`
	Credential string `json:"credential"`
	Mode       string `json:"mode"`
}

// dockerfilePreviewResponse answers one preview: the commit the content was
// read at (empty when the reader could not resolve it), whether the path
// named a file, and the file's content (empty when it did not).
type dockerfilePreviewResponse struct {
	Commit  string `json:"commit"`
	Exists  bool   `json:"exists"`
	Content string `json:"content"`
}

// previewCache bounds: entries are keyed by user-supplied (url, subdir,
// commit, path, credential) tuples and a dockerfile preview should be
// small, but a long-lived host must not grow without limit — so the cache
// keeps at most maxEntries answers and never stores payloads over maxBytes
// (oversized answers are still served, just not memoized).
const (
	previewCacheMaxEntries = 256
	previewCacheMaxBytes   = 1 << 20
)

// cacheEntry is one LRU element: the key rides along so eviction can drop
// the map side.
type cacheEntry struct {
	key  string
	resp dockerfilePreviewResponse
}

// previewCache is a small mutex-guarded LRU behind get/put.
type previewCache struct {
	mu   sync.Mutex
	keys *list.List // front = most recently used
	vals map[string]*list.Element
}

var thePreviewCache = &previewCache{keys: list.New(), vals: map[string]*list.Element{}}

func (c *previewCache) get(key string) (dockerfilePreviewResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.vals[key]
	if !ok {
		return dockerfilePreviewResponse{}, false
	}
	c.keys.MoveToFront(el)
	return el.Value.(cacheEntry).resp, true
}

func (c *previewCache) put(key string, resp dockerfilePreviewResponse) {
	if len(resp.Content) > previewCacheMaxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.vals[key]; ok {
		el.Value = cacheEntry{key, resp}
		c.keys.MoveToFront(el)
		return
	}
	c.vals[key] = c.keys.PushFront(cacheEntry{key, resp})
	for c.keys.Len() > previewCacheMaxEntries {
		oldest := c.keys.Back()
		if oldest == nil {
			break
		}
		c.keys.Remove(oldest)
		delete(c.vals, oldest.Value.(cacheEntry).key)
	}
}

// previewCacheKey includes the credential ref: entries fetched through a
// credential only ever answer requests naming that same ref (which pass the
// same usage gate as a fresh fetch), so a cached private-repo answer can
// never surface to an anonymous or differently-credentialed caller.
func previewCacheKey(url, subdir, commit, path, credential string) string {
	return url + "\x00" + subdir + "\x00" + commit + "\x00" + path + "\x00" + credential
}

// previewPathDeny lists characters the probe's inlined COPY would treat as
// syntax (wildcards, env expansion, quoting) plus a leading dash (COPY flag
// position): a real build reads the dockerfile by its literal filename, so
// paths carrying them cannot be previewed faithfully. fleet.ProbeFile
// guards the same set module-side.
const previewPathDeny = `*?[$\"'`

// handleDockerfilePreview fetches one dockerfile's content at a ref for the
// build editor's preview: resolve the ref to a commit (ls-remote, with the
// request's credential), serve the memoized answer when possible, and on a
// miss fetch per the source's mode — runtime probes through a fleet solve
// (identical context resolution to a real build), server/empty shallow
// clones host-side and reads the single file. The response's commit is only
// ever the one the fetch actually read at; an unresolvable commit stays
// empty and the answer is not cached.
func (d *Deps) handleDockerfilePreview(c *gin.Context) {
	var req dockerfilePreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request body: %v", err)})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	req.Subdir = strings.TrimSpace(req.Subdir)
	req.Path = strings.TrimSpace(req.Path)
	req.Credential = strings.TrimSpace(req.Credential)
	switch {
	case req.URL == "":
		c.JSON(http.StatusBadRequest, gin.H{"error": "url is required"})
		return
	case req.Path == "":
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is required"})
		return
	case strings.ContainsFunc(req.Path, unicode.IsSpace):
		// One spec field, two wire shapes: whitespace means the value is
		// inline Dockerfile content, which has nothing to preview.
		c.JSON(http.StatusBadRequest, gin.H{"error": "path must be a context-relative file path, not inline dockerfile content"})
		return
	case strings.ContainsAny(req.Path, previewPathDeny) || strings.HasPrefix(req.Path, "-"):
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("path %q carries characters the probe's inlined COPY would reinterpret (wildcard, env, quote or flag syntax) and cannot be previewed faithfully", req.Path)})
		return
	case req.Mode != "" && req.Mode != "server" && req.Mode != "runtime":
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("mode must be server or runtime, got %q", req.Mode)})
		return
	}
	// Runtime mode drives a full fleet solve — runtime lease, buildkitd
	// fetching the repo, dockerfile evaluation — the same resource profile
	// as POST /builds, so it carries the same create verb; the host-side
	// read stays a get.
	verb := "get"
	if req.Mode == "runtime" {
		verb = "create"
	}
	if !authorize(c, verb) {
		return
	}
	if req.Credential != "" && !canUseCredentials(c) {
		return
	}

	src := gitctx.Source{URL: req.URL, Ref: req.Ref, Subdir: req.Subdir}
	if req.Credential != "" {
		if d.creds == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("credential %q cannot be resolved: credential center is not wired", req.Credential)})
			return
		}
		freq := fleet.Request{Git: &src}
		if err := d.creds.InjectFleetCredentials(&freq, req.Credential, ""); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		src = *freq.Git
	}

	// Best-effort daemon-side resolution: it only routes the cache lookup.
	// In runtime mode the daemon may not reach the git server at all, and
	// the probe itself resolves (and reports) the commit it read.
	commit := gitctx.ResolveCommit(src)
	if commit != "" {
		if resp, ok := thePreviewCache.get(previewCacheKey(req.URL, req.Subdir, commit, req.Path, req.Credential)); ok {
			c.JSON(http.StatusOK, resp)
			return
		}
	}

	var resp dockerfilePreviewResponse
	var err error
	if req.Mode == "runtime" {
		probeSrc := src
		probeSrc.Mode = "runtime"
		resp, err = d.probeDockerfile(c.Request.Context(), probeSrc, req.Path)
	} else {
		resp, err = d.readDockerfileHostSide(c.Request.Context(), src, req.Path)
	}
	if err != nil {
		// Source errors embed the client-supplied URL, which may carry
		// basic-auth userinfo.
		c.JSON(http.StatusInternalServerError, gin.H{"error": credentialsadapter.RedactCredentials(err.Error())})
		return
	}
	if resp.Commit != "" {
		thePreviewCache.put(previewCacheKey(req.URL, req.Subdir, resp.Commit, req.Path, req.Credential), resp)
	}
	c.JSON(http.StatusOK, resp)
}

// probeDockerfile fetches the file through a fleet probe solve: the probe
// reuses the build's source/context assembly, so the path it validates is
// the path a build would read, and the commit it reports is the one
// buildkitd actually resolved.
func (d *Deps) probeDockerfile(ctx context.Context, src gitctx.Source, path string) (dockerfilePreviewResponse, error) {
	res, err := d.app.Fleet.ProbeFile(ctx, fleet.Request{Git: &src}, path)
	if err != nil {
		return dockerfilePreviewResponse{}, err
	}
	return dockerfilePreviewResponse{Commit: res.Commit, Exists: res.Exists, Content: string(res.Content)}, nil
}

// readDockerfileHostSide fetches the file with a host-side shallow clone of
// the original ref (the resolved SHA is never passed down: planClone would
// turn it into a full clone).
func (d *Deps) readDockerfileHostSide(ctx context.Context, src gitctx.Source, path string) (dockerfilePreviewResponse, error) {
	fr, err := gitctx.ReadFile(ctx, src, path)
	if err != nil {
		return dockerfilePreviewResponse{}, err
	}
	return dockerfilePreviewResponse{Commit: fr.Commit, Exists: fr.Found, Content: string(fr.Content)}, nil
}
