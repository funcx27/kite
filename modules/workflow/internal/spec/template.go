package spec

import (
	"fmt"
	"slices"
	"strings"
)

// ResolveEnv is the data a template resolves against: run parameters, outputs
// of already-succeeded steps, and run-level variables (design §4/§5.4).
type ResolveEnv struct {
	Params map[string]any
	// Steps maps node id to its Outputs for succeeded nodes only; referencing
	// an absent node (not succeeded or unknown) is a resolution failure.
	Steps map[string]map[string]any
	Run   map[string]any
	// Git holds git-scope variables, populated only for build-image nodes
	// with a git source (commit resolved via ls-remote before the full
	// render; nil for every other node).
	Git map[string]any
	// Ctx holds workflow-scope variables (workflow, space, registry) —
	// derived from the workflow itself, so every node resolves them.
	Ctx map[string]any
	// Build holds build-scope variables (fingerprint), populated only for
	// build-image nodes whose props reference build.* — the fingerprint needs
	// a dockerfile probe before the full render; nil for every other node.
	Build map[string]any
}

// TemplateRefs lists every {{ path }} reference appearing in the props tree,
// in encounter order, duplicates included.
func TemplateRefs(props Props) []string {
	var refs []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			for _, m := range templatePattern.FindAllStringSubmatch(t, -1) {
				refs = append(refs, m[1])
			}
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				walk(t[k])
			}
		case []any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	walk(map[string]any(props))
	return refs
}

// Resolve renders every template token in the props tree against env. A token
// that cannot be resolved is an error naming the missing reference — the
// engine turns it into a node failure (design §4).
func Resolve(props Props, env ResolveEnv) (Props, error) {
	if len(props) == 0 {
		return props, nil
	}
	resolved, err := resolveValue(map[string]any(props), env)
	if err != nil {
		return nil, err
	}
	return resolved.(map[string]any), nil
}

func resolveValue(v any, env ResolveEnv) (any, error) {
	switch t := v.(type) {
	case string:
		return resolveString(t, env)
	case map[string]any:
		out := make(map[string]any, len(t))
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			rv, err := resolveValue(t[k], env)
			if err != nil {
				return nil, err
			}
			out[k] = rv
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			rv, err := resolveValue(item, env)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	default:
		return v, nil
	}
}

// resolveString replaces each {{ path }} token. A string that is exactly one
// token keeps the referenced value's type (number/bool pass through); a mixed
// string interpolates tokens as text.
func resolveString(s string, env ResolveEnv) (any, error) {
	matches := templatePattern.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s, nil
	}
	if len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(s) {
		v, err := lookup(s[matches[0][2]:matches[0][3]], env)
		if err != nil {
			return nil, err
		}
		return v, nil
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m[0]])
		v, err := lookup(s[m[2]:m[3]], env)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%v", v)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String(), nil
}

// lookup resolves one dotted path: params.<name>, steps.<id>.outputs.<key>
// or run.<variable> — two-level lookups only, no expression engine.
func lookup(path string, env ResolveEnv) (any, error) {
	seg := strings.Split(path, ".")
	switch {
	case len(seg) == 2 && seg[0] == "params":
		v, ok := env.Params[seg[1]]
		if !ok {
			return nil, fmt.Errorf("template references unknown parameter %q", path)
		}
		return v, nil
	case len(seg) == 4 && seg[0] == "steps" && seg[2] == "outputs":
		outputs, ok := env.Steps[seg[1]]
		if !ok {
			return nil, fmt.Errorf("template references step %q which has not succeeded", seg[1])
		}
		v, ok := outputs[seg[3]]
		if !ok {
			return nil, fmt.Errorf("template references missing output %q of step %q", path, seg[1])
		}
		return v, nil
	case len(seg) == 2 && seg[0] == "run":
		v, ok := env.Run[seg[1]]
		if !ok {
			return nil, fmt.Errorf("template references unknown run variable %q", path)
		}
		return v, nil
	case len(seg) == 2 && seg[0] == "git":
		if env.Git == nil {
			return nil, fmt.Errorf("template references git variable %q but the node has no resolved git source", path)
		}
		v, ok := env.Git[seg[1]]
		if !ok {
			return nil, fmt.Errorf("template references unknown git variable %q", path)
		}
		return v, nil
	case len(seg) == 2 && seg[0] == "ctx":
		if env.Ctx == nil {
			return nil, fmt.Errorf("template references ctx variable %q but none are available", path)
		}
		v, ok := env.Ctx[seg[1]]
		if !ok {
			return nil, fmt.Errorf("template references unknown ctx variable %q", path)
		}
		return v, nil
	case len(seg) == 2 && seg[0] == "build":
		if env.Build == nil {
			return nil, fmt.Errorf("template references build variable %q but the node has no resolved build context", path)
		}
		v, ok := env.Build[seg[1]]
		if !ok {
			return nil, fmt.Errorf("template references unknown build variable %q", path)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("malformed template reference %q (want params.<name>, steps.<id>.outputs.<key>, run.<number|status|url>, git.<commit|shortCommit>, ctx.<workflow|space|registry> or build.<fingerprint>)", path)
	}
}
