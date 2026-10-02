package spec

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// ResolveParams validates a trigger's raw parameter map against the schema
// and returns the effective snapshot: required values must be present and
// type-correct, optional ones fall back to their defaults, enum values must
// be one of the options (design §4/§7).
func ResolveParams(params []ParamSpec, input map[string]any) (map[string]any, error) {
	resolved := make(map[string]any, len(params))
	for _, p := range params {
		v, present := input[p.Name]
		if !present || v == nil {
			if p.Required && p.Default == nil {
				return nil, fmt.Errorf("parameter %q is required", p.Name)
			}
			v = p.Default
			if v == nil {
				continue
			}
		}
		checked, err := checkParam(p, v)
		if err != nil {
			return nil, err
		}
		resolved[p.Name] = checked
	}
	return resolved, nil
}

func checkParam(p ParamSpec, v any) (any, error) {
	switch p.Type {
	case ParamString, ParamEnum:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("parameter %q must be a string", p.Name)
		}
		if p.Type == ParamEnum && !containsValue(p.Options, s) {
			return nil, fmt.Errorf("parameter %q must be one of %v", p.Name, p.Options)
		}
		return s, nil
	case ParamNumber:
		n, ok := toNumber(v)
		if !ok {
			return nil, fmt.Errorf("parameter %q must be a number", p.Name)
		}
		return n, nil
	case ParamBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("parameter %q must be a boolean", p.Name)
		}
		return b, nil
	default:
		// Schema validated the type at save time; tolerate pass-through.
		return v, nil
	}
}

// toNumber normalizes JSON numbers (float64) and integer literals.
func toNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func containsValue(options []any, v any) bool {
	for _, o := range options {
		if reflect.DeepEqual(normalizeNumber(o), normalizeNumber(v)) {
			return true
		}
	}
	return false
}

// normalizeNumber folds integer literals and JSON numbers so enum options
// compare equal regardless of their literal form.
func normalizeNumber(v any) any {
	if n, ok := toNumber(v); ok {
		return n
	}
	return v
}
