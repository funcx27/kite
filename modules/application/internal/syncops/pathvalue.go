package syncops

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/funcx27/kite/modules/application/internal/store"
)

// deleteValue is the SetPathValue sentinel that removes the key at path
// instead of writing a value.
type deleteValue struct{}

// DeleteValue adopts a live deletion: SetPathValue removes the key at path.
var DeleteValue = deleteValue{}

// PathValue resolves a diff path ("spec.replicas",
// "spec.template.spec.containers[name=app].image") against the document tree.
// Dotted map keys (annotation keys) are resolved greedily: an exact token
// wins, otherwise the first joined candidate key that exists. found is false
// when the path does not exist.
func PathValue(root map[string]interface{}, path string) (value interface{}, found bool) {
	if path == "" {
		return nil, false
	}
	cur := interface{}(root)
	tokens := strings.Split(path, ".")
	for i := 0; i < len(tokens); {
		m, ok := store.AsMap(cur)
		if !ok {
			return nil, false
		}
		key, idx, name, hasSelector := splitSelector(tokens[i])
		if hasSelector {
			v, exists := m[key]
			if !exists {
				return nil, false
			}
			list, ok := store.AsList(v)
			if !ok {
				return nil, false
			}
			elem, _ := findListElem(list, idx, name)
			if elem == nil {
				return nil, false
			}
			cur, i = elem, i+1
			continue
		}
		v, exists := m[key]
		if exists {
			cur, i = v, i+1
			continue
		}
		_, joinedVal, rest, joinedOK := joinedToken(m, tokens[i:])
		if !joinedOK {
			return nil, false
		}
		cur = joinedVal
		i = len(tokens) - len(rest)
	}
	return cur, true
}

// SetPathValue writes value at the diff path into the document tree, creating
// intermediate maps as needed. List selectors match [name=x] or [N]; a
// targeted list element missing from the tree inserts value whole (append).
// The DeleteValue sentinel removes the key at path; deleting an absent path
// is a no-op, so adoptions stay idempotent. Paths must come from
// DiffManifests output for the same document, so dotted keys resolve exactly
// like PathValue.
func SetPathValue(root map[string]interface{}, path string, value interface{}) error {
	if path == "" {
		return fmt.Errorf("setPathValue: empty path")
	}
	if _, ok := value.(deleteValue); ok {
		if err := deletePathValue(interface{}(root), strings.Split(path, ".")); err != nil {
			return fmt.Errorf("setPathValue: %s: %w", path, err)
		}
		return nil
	}
	if err := setPathValue(interface{}(root), strings.Split(path, "."), value, path); err != nil {
		return fmt.Errorf("setPathValue: %s: %w", path, err)
	}
	return nil
}

func setPathValue(cur interface{}, tokens []string, value interface{}, path string) error {
	m, ok := store.AsMap(cur)
	if !ok {
		return fmt.Errorf("parent of %q is not an object", tokens[0])
	}
	key, idx, name, hasSelector := splitSelector(tokens[0])
	if hasSelector {
		list, ok := store.AsList(m[key])
		if !ok {
			list = []interface{}{}
			m[key] = list
		}
		elem, elemIdx := findListElem(list, idx, name)
		if len(tokens) > 1 {
			if elem == nil {
				return fmt.Errorf("element %q missing below %q", tokens[0], path)
			}
			return setPathValue(elem, tokens[1:], value, path)
		}
		if elem != nil {
			list[elemIdx] = value
			return nil
		}
		// Missing target element: insert the value whole.
		m[key] = append(list, value)
		return nil
	}
	if len(tokens) == 1 {
		m[key] = value
		return nil
	}
	v, exists := m[key]
	if !exists {
		joined, joinedVal, rest, joinedOK := joinedToken(m, tokens)
		if joinedOK {
			if len(rest) == 0 {
				// The whole remaining path is one dotted key.
				m[joined] = value
				return nil
			}
			return setPathValue(joinedVal, rest, value, path)
		}
		v = map[string]interface{}{}
		m[key] = v
	}
	return setPathValue(v, tokens[1:], value, path)
}

// deletePathValue removes the key at path; absent paths are a no-op.
func deletePathValue(cur interface{}, tokens []string) error {
	m, ok := store.AsMap(cur)
	if !ok {
		return nil
	}
	key, idx, name, hasSelector := splitSelector(tokens[0])
	if hasSelector {
		list, ok := store.AsList(m[key])
		if !ok {
			return nil
		}
		elem, elemIdx := findListElem(list, idx, name)
		if elem == nil {
			return nil
		}
		if len(tokens) > 1 {
			return deletePathValue(elem, tokens[1:])
		}
		m[key] = append(list[:elemIdx], list[elemIdx+1:]...)
		return nil
	}
	if len(tokens) == 1 {
		delete(m, key)
		return nil
	}
	v, exists := m[key]
	if !exists {
		joined, joinedVal, rest, joinedOK := joinedToken(m, tokens)
		if !joinedOK {
			return nil
		}
		if len(rest) == 0 {
			// The whole remaining path is one dotted key.
			delete(m, joined)
			return nil
		}
		return deletePathValue(joinedVal, rest)
	}
	return deletePathValue(v, tokens[1:])
}

// joinedToken resolves a greedy dotted key over the remaining tokens: the
// first joined candidate tokens[0:j+1] (j ascending, matching PathValue) that
// exists in m, returned with its key, value and the tokens after it.
func joinedToken(m map[string]interface{}, tokens []string) (key string, value interface{}, rest []string, found bool) {
	for j := 1; j < len(tokens); j++ {
		cand := strings.Join(tokens[:j+1], ".")
		if v, exists := m[cand]; exists {
			return cand, v, tokens[j+1:], true
		}
	}
	return "", nil, nil, false
}

// findListElem finds a list element by its name key or index; elemIdx is the
// element's position, -1 when absent.
func findListElem(list []interface{}, idx int, name string) (elem interface{}, elemIdx int) {
	if name != "" {
		for i, e := range list {
			if em, ok := store.AsMap(e); ok && em["name"] == name {
				return e, i
			}
		}
		return nil, -1
	}
	if idx >= 0 && idx < len(list) {
		return list[idx], idx
	}
	return nil, -1
}

// splitSelector splits a trailing "[N]" / "[name=x]" off a path token.
func splitSelector(token string) (key string, idx int, name string, has bool) {
	i := strings.Index(token, "[")
	if i < 0 || !strings.HasSuffix(token, "]") {
		return token, -1, "", false
	}
	key = token[:i]
	inner := token[i+1 : len(token)-1]
	if strings.HasPrefix(inner, "name=") {
		return key, -1, strings.TrimPrefix(inner, "name="), true
	}
	n, err := strconv.Atoi(inner)
	if err != nil || n < 0 {
		return token, -1, "", false
	}
	return key, n, "", true
}
