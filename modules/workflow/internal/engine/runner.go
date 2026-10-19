package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/funcx27/kite/modules/workflow/exec"
	"github.com/funcx27/kite/modules/workflow/internal/spec"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// NodeRunner executes one task-node type. Decoding and checking the rendered
// props is Run's first step — save-time validation lives in spec.Validate, so
// the interface carries no ValidateProps. Approval is engine-native
// (event-driven, design §5.3) and never reaches a runner. wf is the owning
// workflow (auto-naming seeds the image path with its name when the props
// carry no app).
type NodeRunner interface {
	Run(ctx context.Context, rendered spec.Props, run store.WorkflowRun, wf store.Workflow, onLog func(string)) (map[string]any, error)
}

// buildDeps is the capability slice buildRunner consumes.
type buildDeps interface {
	AuthorizeUser(userID uint, resource, verb, cluster, namespace string) bool
	BuildImage(ctx context.Context, req exec.BuildRequest, onLog func(string)) (exec.BuildResult, error)
	// ProviderFor resolves the namespace's build-cluster default; ok=false
	// when nothing is configured.
	ProviderFor(namespace string) (string, bool)
	// DefaultRegistry resolves the credential center's default registry
	// pointer; ok=false when no valid default exists. Drives
	// applyDefaultRegistry and build outputRef auto-naming.
	DefaultRegistry() (credential, host string, ok bool)
	// ResolveGitCommit resolves a git ref to its commit SHA via one ls-remote
	// round trip; a resolution failure returns an error (node failure).
	ResolveGitCommit(ctx context.Context, url, ref, gitCredentialRef string) (string, error)
	// ProbeDockerfile fetches one dockerfile from a git context (server
	// mode: host-side shallow clone; runtime mode: fleet probe solve, whose
	// commit is the one buildkitd itself resolved).
	ProbeDockerfile(ctx context.Context, url, ref, subdir, path, mode, gitCredentialRef string) (exec.DockerfileProbe, error)
}

// buildRunner drives a build-image node (design §5.1).
type buildRunner struct {
	deps buildDeps
}

// credentialsResource is the credential center pseudo-resource: a build
// referencing a credential consumes that credential, which requires read
// access on the center (usage authorization, design §9) beyond the builds
// capability.
const credentialsResource = "credentials"

// Run implements NodeRunner for spec.NodeBuildImage.
func (r buildRunner) Run(ctx context.Context, rendered spec.Props, run store.WorkflowRun, wf store.Workflow, onLog func(string)) (map[string]any, error) {
	// Node-level authorization (design §8): the run executes with its
	// trigger user's RBAC identity; missing permission fails the node
	// loudly rather than skipping it. Buildfleet builds carry no cluster in
	// M1 props, so the check targets the host-wide builds capability — an
	// empty cluster only matches wildcard-cluster grants until M2 threads a
	// cluster through the build props.
	if !r.deps.AuthorizeUser(run.TriggeredBy, "builds", "create", "", "") {
		return nil, fmt.Errorf("user %d does not have permission to create builds", run.TriggeredBy)
	}
	var p spec.BuildProps
	if err := decodeProps(rendered, &p); err != nil {
		return nil, fmt.Errorf("decode build props: %w", err)
	}
	// Referencing a credential consumes its material — the same gate the
	// REST build path applies (credentials:get, host-wide). Each non-empty
	// ref is checked separately so the error names the one that failed.
	for _, ref := range []string{p.GitCredentialRef, p.RegistryCredentialRef} {
		if ref == "" {
			continue
		}
		if !r.deps.AuthorizeUser(run.TriggeredBy, credentialsResource, "get", "", "") {
			return nil, fmt.Errorf("user %d does not have permission to use credential %q", run.TriggeredBy, ref)
		}
	}
	var source *exec.GitSource
	var httpSource *exec.HTTPSource
	switch {
	case p.Git != nil && p.Git.URL != "":
		source = &exec.GitSource{URL: p.Git.URL, Ref: p.Git.Ref, Subdir: p.Git.Subdir, Mode: p.Git.Mode}
	case p.HTTP != nil && p.HTTP.URL != "":
		httpSource = &exec.HTTPSource{URL: p.HTTP.URL}
	default:
		return nil, fmt.Errorf("build node needs a git or http source")
	}
	fingerprint := p.ResolvedFingerprint
	if p.OutputRef == "" {
		fp, err := autoOutputRef(ctx, r.deps, &p, wf.Name, onLog)
		if err != nil {
			return nil, err
		}
		fingerprint = fp
	}
	// Commit pin-back (a pin is set by auto-naming above, or by the engine
	// stamping the git-var resolution onto the source): the build must
	// produce the commit the tag or template rendered with. Server mode
	// keeps the original ref (a SHA ref would downgrade planClone to a full
	// clone) and verifies the checked-out HEAD; runtime mode replaces the
	// ref with the exact SHA, so buildkitd's own resolution cannot race.
	if source != nil && p.Git.PinnedCommit != "" {
		if source.Mode == "runtime" {
			source.Ref = p.Git.PinnedCommit
		} else {
			source.PinnedCommit = p.Git.PinnedCommit
		}
	}
	applyDefaultRegistry(r.deps, run.TriggeredBy, &p)
	// One spec field, two wire shapes (backend-authoritative rule): any
	// whitespace inside the trimmed dockerfile value means inline Dockerfile
	// content, a single token is a context path, empty falls back to the
	// conventional "Dockerfile" path.
	dockerfile, dockerfileContent := "", ""
	switch v := strings.TrimSpace(p.Dockerfile); {
	case strings.ContainsFunc(v, unicode.IsSpace):
		dockerfileContent = v
	case v != "":
		dockerfile = v
	default:
		dockerfile = "Dockerfile"
	}
	// The node-level provider keeps priority; an empty one falls back to the
	// node space's build-cluster default (exact match, then the global
	// default row).
	provider := p.Provider
	if provider == "" {
		if def, ok := r.deps.ProviderFor(p.Space); ok {
			provider = def
		}
	}
	res, err := r.deps.BuildImage(ctx, exec.BuildRequest{
		Git:                   source,
		HTTP:                  httpSource,
		Dockerfile:            dockerfile,
		DockerfileContent:     dockerfileContent,
		Args:                  p.Args,
		Platforms:             p.Platforms,
		OutputRef:             p.OutputRef,
		GitCredentialRef:      p.GitCredentialRef,
		RegistryCredentialRef: p.RegistryCredentialRef,
		Provider:              provider,
	}, onLog)
	if err != nil {
		return nil, err
	}
	return buildOutputs(res, fingerprint), nil
}

// applyDefaultRegistry completes the build props from the credential
// center's default registry pointer (one source for both): a bare outputRef
// — no registry part in its first segment — prefixes the default host, and
// an empty registryCredentialRef falls back to the default push credential
// behind the same usage gate an explicit ref passes (credentials:get), so an
// unauthorized user keeps the anonymous push instead of failing or silently
// escalating. Without a valid default the props stay untouched.
func applyDefaultRegistry(deps buildDeps, userID uint, p *spec.BuildProps) {
	bare := needsRegistryPrefix(p.OutputRef)
	if !bare && p.RegistryCredentialRef != "" {
		return
	}
	credential, host, ok := deps.DefaultRegistry()
	if !ok {
		return
	}
	if bare {
		p.OutputRef = host + "/" + p.OutputRef
	}
	if p.RegistryCredentialRef == "" &&
		deps.AuthorizeUser(userID, credentialsResource, "get", "", "") {
		p.RegistryCredentialRef = credential
	}
}

// needsRegistryPrefix reports whether ref's first path segment carries no
// registry part: no dot (domain), no colon (port) and not localhost — the
// markers Docker's reference normalization uses to tell a registry from a
// bare image name.
func needsRegistryPrefix(ref string) bool {
	first := ref
	if i := strings.Index(ref, "/"); i >= 0 {
		first = ref[:i]
	}
	return !strings.Contains(first, ".") && !strings.Contains(first, ":") && first != "localhost"
}

// imageTagPattern is the registry tag grammar (docker distribution):
// [A-Za-z0-9_][A-Za-z0-9_.-]{0,127}.
var imageTagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// imageRepoSegmentPattern is one lowercase path component of an image
// reference (docker distribution), used to judge name-derived path seeds.
var imageRepoSegmentPattern = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)

// autoOutputRef fills an empty outputRef with the content-addressed default
// name <default-host>/<space>/<app>:<short-commit>[-<variant>]. The app
// segment falls back to the workflow name; a tag template, when set, is the
// tag verbatim (no variant appended). Any missing prerequisite keeps the
// explicit-outputRef error, naming what is absent. Auto-naming is an
// implicit git-variable use: it resolves the commit itself (reusing a pin
// the engine already stamped for {{git.*}} renders), independent of the
// explicit usesGitVars gate. The returned fingerprint feeds the node outputs.
func autoOutputRef(ctx context.Context, deps buildDeps, p *spec.BuildProps, wfName string, onLog func(string)) (string, error) {
	_, host, ok := deps.DefaultRegistry()
	var missing []string
	if !ok {
		missing = append(missing, "a default registry")
	}
	if p.Space == "" {
		missing = append(missing, "space")
	}
	app := p.App
	if app == "" {
		app = strings.ToLower(wfName)
	}
	if app == "" {
		missing = append(missing, "app")
	} else if p.App == "" && !imageRepoSegmentPattern.MatchString(app) {
		missing = append(missing, "an app (the workflow name cannot seed the image path)")
	}
	if p.Git == nil || p.Git.URL == "" {
		missing = append(missing, "a git source (the tag carries its commit)")
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("build node needs outputRef (auto-naming is missing %s)", strings.Join(missing, ", "))
	}
	commit, content, err := resolveAutoNameInputs(ctx, deps, p, onLog)
	if err != nil {
		return "", err
	}
	tag, fingerprint := "", variantFingerprint(content, p.Args, p.Git.Subdir)
	if p.TagTemplate != "" {
		// The template already rendered (engine pre-resolved build.fingerprint
		// when referenced); prefer that value — it is the one the tag carries.
		tag = p.TagTemplate
		if p.ResolvedFingerprint != "" {
			fingerprint = p.ResolvedFingerprint
		}
		if !imageTagPattern.MatchString(tag) {
			return "", fmt.Errorf("tag template rendered to %q, which is not a valid image tag", tag)
		}
	} else {
		tag = commit[:min(7, len(commit))]
		if fingerprint != "" {
			tag += "-" + fingerprint
		}
	}
	p.OutputRef = host + "/" + p.Space + "/" + app + ":" + tag
	p.Git.PinnedCommit = commit
	if onLog != nil {
		onLog(fmt.Sprintf("auto-named output image %s\n", p.OutputRef))
	}
	return fingerprint, nil
}

// resolveAutoNameInputs resolves the tag commit and the dockerfile content
// the variant fingerprint hashes. Content discipline: the fingerprint hashes
// the dockerfile content, never its path — a path that cannot be fetched
// (missing file, unreachable repo) fails the node instead of degrading to a
// path-derived tag. Runtime mode always probes through a fleet solve: the
// daemon may have no egress to the git server (buildkitd does), and the
// probe's commit is by construction the one the following build fetches.
func resolveAutoNameInputs(ctx context.Context, deps buildDeps, p *spec.BuildProps, onLog func(string)) (commit, content string, err error) {
	v := strings.TrimSpace(p.Dockerfile)
	inline := strings.ContainsFunc(v, unicode.IsSpace)
	runtime := p.Git.Mode == "runtime"
	path := v
	if path == "" {
		path = "Dockerfile"
	}
	// Inline content needs no fetch; runtime mode still probes so the
	// commit comes from buildkitd's own resolution. The commit-only probe
	// targets the conventional path — its content is ignored.
	var probe *exec.DockerfileProbe
	if !inline || runtime {
		probePath := path
		if inline {
			probePath = "Dockerfile"
		}
		if onLog != nil {
			onLog(fmt.Sprintf("auto-naming: fetching dockerfile %q\n", probePath))
		}
		pr, err := deps.ProbeDockerfile(ctx, p.Git.URL, p.Git.Ref, p.Git.Subdir, probePath, p.Git.Mode, p.GitCredentialRef)
		if err != nil {
			return "", "", fmt.Errorf("fetch dockerfile for auto-naming: %w", err)
		}
		probe = &pr
	}
	if !inline {
		if !probe.Exists {
			return "", "", fmt.Errorf("auto-naming requires the dockerfile content, but %q was not found in the build context", path)
		}
		content = probe.Content
	} else {
		content = v
	}
	switch {
	case p.Git.PinnedCommit != "":
		// Reuse the resolution the engine already ran for {{git.*}} renders.
		commit = p.Git.PinnedCommit
	case runtime:
		commit = probe.Commit
	default:
		c, err := deps.ResolveGitCommit(ctx, p.Git.URL, p.Git.Ref, p.GitCredentialRef)
		if err != nil {
			return "", "", fmt.Errorf("resolve git commit for auto-naming: %w", err)
		}
		commit = c
	}
	if commit == "" {
		shownRef := p.Git.Ref
		if shownRef == "" {
			shownRef = "HEAD"
		}
		// No git URL in the error: it may carry basic-auth userinfo, and
		// node errors persist raw to NodeRun.error — the host-side
		// redactor lives in a package this module cannot import.
		return "", "", fmt.Errorf("auto-naming could not resolve the commit at ref %s", shownRef)
	}
	// The fingerprint must describe the artifact: when the dockerfile was
	// read at a different commit than the tag carries, the ref moved
	// mid-dispatch — fail deterministically; a retry re-resolves both.
	if probe != nil && probe.Commit != "" && probe.Commit != commit {
		return "", "", fmt.Errorf("git ref moved while auto-naming (dockerfile read at %s, commit resolved to %s); retry the run", probe.Commit, commit)
	}
	return commit, content, nil
}

// variantFingerprint derives the auto-name tag's variant part from the build
// inputs that change the produced image beyond its commit: the dockerfile
// content, the template-rendered build args (key-sorted) and the git subdir.
// Fields that never change the artifact are deliberately excluded —
// platforms, provider, resources; any future prop that alters the image MUST
// join this hash, or one tag will name two different images. All inputs
// empty → empty string (a clean <short-commit> tag).
func variantFingerprint(dockerfileContent string, args map[string]string, subdir string) string {
	if dockerfileContent == "" && subdir == "" && len(args) == 0 {
		return ""
	}
	h := sha256.New()
	write := func(s string) { fmt.Fprintf(h, "%d:%s", len(s), s) }
	write(dockerfileContent)
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		write(k)
		write(args[k])
	}
	write(subdir)
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// buildOutputs shapes the build executor result into the node Outputs; the
// digest-carrying reference is the one downstream deploy nodes should
// consume (design §5.1). fingerprint is the variant identity of the produced
// image when the runner knows one (auto-naming, or an engine-resolved
// build.fingerprint); absent for explicitly named builds nobody fingerprinted.
func buildOutputs(res exec.BuildResult, fingerprint string) map[string]any {
	out := map[string]any{
		"imageRef": res.ImageRef,
		"digest":   res.Digest,
	}
	if fingerprint != "" {
		out["fingerprint"] = fingerprint
	}
	if res.Commit != "" {
		out["commit"] = res.Commit
	}
	if res.ImageRef != "" && res.Digest != "" {
		base := res.ImageRef
		if i := strings.LastIndex(base, ":"); i > strings.LastIndex(base, "/") {
			base = base[:i]
		}
		out["imageRefWithDigest"] = base + "@" + res.Digest
	}
	return out
}

// deployDeps is the capability slice deployRunner consumes.
type deployDeps interface {
	DeployApp(ctx context.Context, req exec.DeployRequest, onLog func(string)) (exec.DeployResult, error)
}

// deployRunner drives a deploy-app node (design §5.2). The run executes with
// its trigger user's RBAC identity (design §7).
type deployRunner struct {
	deps deployDeps
}

// Run implements NodeRunner for spec.NodeDeployApp.
func (r deployRunner) Run(ctx context.Context, rendered spec.Props, run store.WorkflowRun, _ store.Workflow, onLog func(string)) (map[string]any, error) {
	var p spec.DeployProps
	if err := decodeProps(rendered, &p); err != nil {
		return nil, fmt.Errorf("decode deploy props: %w", err)
	}
	if p.Space == "" || p.App == "" || p.Cluster == "" {
		return nil, fmt.Errorf("deploy node needs space, app and cluster")
	}
	res, err := r.deps.DeployApp(ctx, exec.DeployRequest{
		Space:                 p.Space,
		App:                   p.App,
		Cluster:               p.Cluster,
		Image:                 p.Image,
		RegistryCredentialRef: p.RegistryCredentialRef,
		DryRun:                p.DryRun,
		OperatorID:            run.TriggeredBy,
	}, onLog)
	if err != nil {
		return nil, err
	}
	var preview any = res.Preview
	if len(res.Preview) > 0 {
		var docs any
		if json.Unmarshal(res.Preview, &docs) == nil {
			preview = docs
		}
	}
	return map[string]any{"revision": res.Revision, "preview": preview}, nil
}

// notifyDeps is the capability slice notifyRunner consumes.
type notifyDeps interface {
	Notify(ctx context.Context, req exec.NotifyRequest) error
}

// notifyRunner drives a notify node (design §5.4). The executor's failure
// fails the node but never blocks the pipeline — notify nodes carry forced
// continue semantics.
type notifyRunner struct {
	deps notifyDeps
}

// Run implements NodeRunner for spec.NodeNotify.
func (r notifyRunner) Run(ctx context.Context, rendered spec.Props, run store.WorkflowRun, _ store.Workflow, onLog func(string)) (map[string]any, error) {
	var p spec.NotifyProps
	if err := decodeProps(rendered, &p); err != nil {
		return nil, fmt.Errorf("decode notify props: %w", err)
	}
	if p.URL == "" {
		return nil, fmt.Errorf("notify node needs url")
	}
	payload, err := notifyPayload(p.PayloadTemplate)
	if err != nil {
		return nil, err
	}
	if err := r.deps.Notify(ctx, exec.NotifyRequest{URL: p.URL, Payload: payload}); err != nil {
		return nil, fmt.Errorf("notify: %w", err)
	}
	return map[string]any{"delivered": true}, nil
}

// notifyPayload wraps the rendered template into the webhook body: a valid
// JSON template is sent as-is, plain text becomes {"text": ...}.
func notifyPayload(template string) ([]byte, error) {
	if template == "" {
		return []byte(`{}`), nil
	}
	if json.Valid([]byte(template)) {
		return []byte(template), nil
	}
	b, err := json.Marshal(map[string]string{"text": template})
	if err != nil {
		return nil, err
	}
	return b, nil
}
