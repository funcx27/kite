package registry

import (
	"fmt"
	"strings"
)

// RenderToml renders buildkitd.toml registry sections in the given order.
// Config.Mirrors follows buildkit semantics directly: the mirrors list on
// a host entry is tried first when pulling images of that host.
func RenderToml(cfgs []Config) (string, error) {
	var b strings.Builder
	for i, c := range cfgs {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "[registry.%q]\n", c.Host)
		if c.Insecure {
			b.WriteString("  insecure = true\n")
		}
		if c.HTTP {
			b.WriteString("  http = true\n")
		}
		if len(c.Mirrors) > 0 {
			quoted := make([]string, len(c.Mirrors))
			for j, m := range c.Mirrors {
				quoted[j] = fmt.Sprintf("%q", m)
			}
			fmt.Fprintf(&b, "  mirrors = [%s]\n", strings.Join(quoted, ", "))
		}
	}
	return b.String(), nil
}
