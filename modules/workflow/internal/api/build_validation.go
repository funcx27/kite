package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/funcx27/kite/modules/workflow/exec"
	"github.com/funcx27/kite/modules/workflow/internal/spec"
)

// hasTemplate reports whether a props value still carries an unresolved "{{
// }}" token: such values only settle at run time, so save-time checks cannot
// judge them and skip the whole item instead — templated workflows are a
// legal form and must never be blocked over a variable.
func hasTemplate(v string) bool { return strings.Contains(v, "{{") }

// validateBuildNodes runs the save-time checks over every build-image node of
// a (already spec.Validate-passed) definition and splits the findings into
// blocking problems and advisory warnings. Two layers: inline dockerfile
// content is parsed locally (a parse error is a deterministic build failure,
// always blocking), and the git source is probed with one ls-remote round
// trip — deterministic failures (bad source shape, rejected auth, missing
// repository/ref) block when the node's build path is guaranteed to repeat
// them, environmental ones (network, DNS, timeout) only warn.
func (h *Handler) validateBuildNodes(ctx context.Context, specJSON []byte) (blocking, warnings []string) {
	var dag spec.DAGSpec
	if err := json.Unmarshal(specJSON, &dag); err != nil {
		// decodeDefinition already validated the shape immediately before;
		// a re-unmarshal failure cannot occur.
		return nil, nil
	}
	for _, node := range dag.Nodes {
		if node.Type != spec.NodeBuildImage {
			continue
		}
		problem, warning := h.validateBuildNode(ctx, node)
		blocking = append(blocking, problem...)
		warnings = append(warnings, warning...)
	}
	return blocking, warnings
}

// validateBuildNode checks one build-image node; each return slice holds at
// most one entry naming the node and the concrete reason.
func (h *Handler) validateBuildNode(ctx context.Context, node spec.Node) (blocking, warnings []string) {
	// One spec field, two wire shapes (spec.SplitDockerfile): only inline
	// content parses locally; a "path:" value is judged by the run (or the
	// preview endpoint).
	if _, content := spec.SplitDockerfile(propsString(node.Props, "dockerfile")); content != "" && !hasTemplate(content) {
		if err := h.deps.ParseDockerfile(content); err != nil {
			return []string{fmt.Sprintf("node %q: inline dockerfile does not parse: %v", node.ID, err)}, nil
		}
	}
	git, _ := node.Props["git"].(map[string]any)
	if git == nil {
		return nil, nil
	}
	url, ref := propsString(git, "url"), propsString(git, "ref")
	credRef := propsString(node.Props, "gitCredentialRef")
	if url == "" || hasTemplate(url) || hasTemplate(ref) || hasTemplate(credRef) {
		// No git source, or source fields that only settle at run time.
		return nil, nil
	}
	_, err := h.deps.ResolveGitCommit(ctx, url, ref, credRef)
	if err == nil {
		return nil, nil
	}
	message := fmt.Sprintf("node %q: git source check failed: %v", node.ID, err)
	var check *exec.GitCheckError
	deterministic := errors.As(err, &check) && check.Deterministic
	// Blocking needs a deterministic failure AND a build path that repeats it
	// daemon-side: server/default-mode fetches clone through the same
	// host-side transport, and an empty outputRef makes the node an
	// auto-naming candidate whose commit resolution also runs daemon-side.
	// Pure runtime mode with an explicit outputRef fetches through
	// buildkitd's own egress — the failure may not reproduce there, so any
	// failure in that shape (and every environmental failure) only warns.
	mode, outputRef := propsString(git, "mode"), propsString(node.Props, "outputRef")
	if deterministic && (mode == "" || mode == "server" || outputRef == "") {
		return []string{message}, nil
	}
	return nil, []string{message}
}
