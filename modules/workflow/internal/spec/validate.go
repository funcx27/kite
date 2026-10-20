package spec

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// dns1123Pattern matches an RFC 1123 label: lowercase alphanumerics and
// dashes, starting and ending with an alphanumeric.
var dns1123Pattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// templatePattern captures one {{ path }} occurrence inside a props string.
var templatePattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.]+)\s*\}\}`)

// ValidateDNS1123Label reports whether value is a DNS-1123 label of at most
// maxLength characters.
func ValidateDNS1123Label(value string, maxLength int) bool {
	return len(value) >= 1 && len(value) <= maxLength && dns1123Pattern.MatchString(value)
}

// Validate checks a definition against the design §4 rules and returns one
// message per problem (the API surfaces them as 400 details):
// node ids unique, edges reference existing nodes, acyclic (DFS), at least
// one start node, and template references resolvable against the schema.
func Validate(s DAGSpec, params []ParamSpec) []string {
	var errs []string
	ids := make(map[string]bool, len(s.Nodes))
	for _, n := range s.Nodes {
		if n.ID == "" {
			errs = append(errs, "node id must not be empty")
			continue
		}
		if ids[n.ID] {
			errs = append(errs, fmt.Sprintf("duplicate node id %q", n.ID))
		}
		ids[n.ID] = true
	}
	paramNames := make(map[string]bool, len(params))
	for _, p := range params {
		if p.Name == "" {
			errs = append(errs, "parameter name must not be empty")
			continue
		}
		if paramNames[p.Name] {
			errs = append(errs, fmt.Sprintf("duplicate parameter name %q", p.Name))
		}
		paramNames[p.Name] = true
		switch p.Type {
		case ParamString, ParamNumber, ParamBool, ParamEnum:
		default:
			errs = append(errs, fmt.Sprintf("parameter %q has unknown type %q", p.Name, p.Type))
		}
		if p.Type == ParamEnum && len(p.Options) == 0 {
			errs = append(errs, fmt.Sprintf("enum parameter %q must list options", p.Name))
		}
	}

	for _, e := range s.Edges {
		if !ids[e.From] {
			errs = append(errs, fmt.Sprintf("edge references unknown node %q", e.From))
		}
		if !ids[e.To] {
			errs = append(errs, fmt.Sprintf("edge references unknown node %q", e.To))
		}
	}
	errs = append(errs, validateAcyclic(s, ids)...)

	// At least one start node, unless the definition is an empty draft.
	if len(s.Nodes) > 0 {
		hasStart := false
		for _, n := range s.Nodes {
			if !hasIncoming(s.Edges, n.ID) {
				hasStart = true
				break
			}
		}
		if !hasStart {
			errs = append(errs, "graph has no start node (a cycle covers every node)")
		}
	}

	for _, n := range s.Nodes {
		errs = append(errs, validateTemplates(n, ids, paramNames)...)
		errs = append(errs, validateGitMode(n)...)
	}
	return errs
}

// validateGitMode rejects unknown git fetch modes at save time; an absent or
// empty mode means the default server-side fetch.
func validateGitMode(n Node) []string {
	git, ok := n.Props["git"].(map[string]any)
	if !ok {
		return nil
	}
	mode, ok := git["mode"].(string)
	if !ok {
		return nil
	}
	switch mode {
	case "", "server", "runtime":
		return nil
	default:
		return []string{fmt.Sprintf("node %q: unknown git mode %q", n.ID, mode)}
	}
}

// validateAcyclic runs a DFS from every node and reports the first cycle
// found as one error naming the path.
func validateAcyclic(s DAGSpec, ids map[string]bool) []string {
	adj := map[string][]string{}
	for _, e := range s.Edges {
		if ids[e.From] && ids[e.To] {
			adj[e.From] = append(adj[e.From], e.To)
		}
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var cycle []string
	var found bool
	var visit func(string)
	visit = func(id string) {
		if found {
			return
		}
		color[id] = gray
		stack = append(stack, id)
		for _, next := range adj[id] {
			switch color[next] {
			case gray:
				// Crop the stack to the cycle segment starting at next.
				start := 0
				for i, v := range stack {
					if v == next {
						start = i
						break
					}
				}
				cycle = append(append([]string{}, stack[start:]...), next)
				found = true
				return
			case white:
				visit(next)
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
	}
	for _, n := range s.Nodes {
		if color[n.ID] == white {
			visit(n.ID)
		}
	}
	if found {
		return []string{"graph must be acyclic: " + strings.Join(cycle, " -> ")}
	}
	return nil
}

func hasIncoming(edges []Edge, id string) bool {
	for _, e := range edges {
		if e.To == id {
			return true
		}
	}
	return false
}

// knownNodeTypes is the node-type vocabulary Validate accepts. The four
// built-in types are pre-registered so the spec package is complete on its
// own; the engine mirrors its runner registry into it via RegisterNodeType.
// Registration happens at wiring time — the mutex keeps the exported setter
// race-free against Validate's reads, it is not a license for runtime
// registration.
var (
	nodeTypeMu     sync.RWMutex
	knownNodeTypes = map[string]bool{
		NodeBuildImage: true,
		NodeDeployApp:  true,
		NodeApproval:   true,
		NodeNotify:     true,
	}
)

// RegisterNodeType adds a node type to the validation vocabulary. Call it at
// package init or module wiring time only, before any spec is validated, so
// the vocabulary stays stable at request time.
func RegisterNodeType(nodeType string) {
	nodeTypeMu.Lock()
	defer nodeTypeMu.Unlock()
	knownNodeTypes[nodeType] = true
}

// isKnownNodeType reports whether nodeType is in the validation vocabulary.
func isKnownNodeType(nodeType string) bool {
	nodeTypeMu.RLock()
	defer nodeTypeMu.RUnlock()
	return knownNodeTypes[nodeType]
}

// validateTemplates checks every {{ ... }} reference in the node's props:
// params.<name> must exist in the schema, steps.<id>.outputs.<key> must
// target an existing node (runtime resolves outputs, design §4).
func validateTemplates(n Node, nodeIDs, paramNames map[string]bool) []string {
	var errs []string
	for _, ref := range TemplateRefs(n.Props) {
		seg := strings.Split(ref, ".")
		switch {
		case len(seg) == 2 && seg[0] == "params":
			if !paramNames[seg[1]] {
				errs = append(errs, fmt.Sprintf("node %q: template references unknown parameter %q", n.ID, ref))
			}
		case len(seg) == 4 && seg[0] == "steps" && seg[2] == "outputs":
			if !nodeIDs[seg[1]] {
				errs = append(errs, fmt.Sprintf("node %q: template references unknown step %q", n.ID, ref))
			}
		case len(seg) == 2 && seg[0] == "run":
			switch seg[1] {
			case "number", "status", "url":
			default:
				errs = append(errs, fmt.Sprintf("node %q: unknown run variable %q", n.ID, ref))
			}
		case len(seg) == 2 && seg[0] == "git":
			switch seg[1] {
			case "commit", "shortCommit":
			default:
				errs = append(errs, fmt.Sprintf("node %q: unknown git variable %q", n.ID, ref))
			}
			if _, ok := n.Props["git"].(map[string]any); !ok {
				errs = append(errs, fmt.Sprintf("node %q: git.* template variables require a git source", n.ID))
			}
		case len(seg) == 2 && seg[0] == "ctx":
			switch seg[1] {
			case "workflow", "space", "registry", "app":
			default:
				errs = append(errs, fmt.Sprintf("node %q: unknown ctx variable %q", n.ID, ref))
			}
		case len(seg) == 2 && seg[0] == "build":
			switch seg[1] {
			case "fingerprint":
			default:
				errs = append(errs, fmt.Sprintf("node %q: unknown build variable %q", n.ID, ref))
			}
			if n.Type != NodeBuildImage {
				errs = append(errs, fmt.Sprintf("node %q: build.* template variables require a build-image node", n.ID))
			}
			if _, ok := n.Props["git"].(map[string]any); !ok {
				errs = append(errs, fmt.Sprintf("node %q: build.* template variables require a git source", n.ID))
			}
		default:
			errs = append(errs, fmt.Sprintf("node %q: malformed template reference %q (want params.<name>, steps.<id>.outputs.<key>, run.<number|status|url>, git.<commit|shortCommit>, ctx.<workflow|space|registry|app> or build.<fingerprint>)", n.ID, ref))
		}
	}
	if n.Type != "" && !isKnownNodeType(n.Type) {
		errs = append(errs, fmt.Sprintf("node %q has unknown type %q", n.ID, n.Type))
	}
	switch n.OnFailure {
	case "", OnFailureStop, OnFailureContinue:
	default:
		errs = append(errs, fmt.Sprintf("node %q has unknown onFailure %q", n.ID, n.OnFailure))
	}
	return errs
}
