// Package resource provides build resource quantities and helpers.
package resource

import (
	"fmt"
	"strconv"
	"strings"
)

type Bytes int64

const (
	Kilo Bytes = 1024
	Mega       = 1024 * Kilo
	Giga       = 1024 * Mega
)

var units = map[string]Bytes{"Ki": Kilo, "Mi": Mega, "Gi": Giga, "Ti": 1024 * Giga}

// ParseQuantity parses "<n>" (bytes) or "<n><Ki|Mi|Gi|Ti>".
func ParseQuantity(s string) (Bytes, error) {
	for suf, mult := range units {
		if strings.HasSuffix(s, suf) {
			n, err := strconv.ParseFloat(strings.TrimSuffix(s, suf), 64)
			if err != nil {
				return 0, fmt.Errorf("parse quantity %q: %w", s, err)
			}
			return Bytes(n * float64(mult)), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse quantity %q: unsupported format", s)
	}
	return Bytes(n), nil
}

func (b Bytes) String() string {
	switch {
	case b >= Giga && b%Giga == 0:
		return fmt.Sprintf("%dGi", b/Giga)
	case b >= Mega && b%Mega == 0:
		return fmt.Sprintf("%dMi", b/Mega)
	default:
		return strconv.FormatInt(int64(b), 10)
	}
}

type Resources struct {
	CPU    float64
	Memory Bytes
	// EphemeralStorage bounds scratch space (buildkit snapshots, cache
	// mounts). k8s maps it to ephemeral-storage; docker has no
	// equivalent. A zero capacity value means undeclared (unbounded),
	// unlike CPU/Memory.
	EphemeralStorage Bytes
}

// Fits reports whether r can hold req (zero-value fields are unbounded).
func (r Resources) Fits(req Resources) bool {
	if req.CPU > r.CPU {
		return false
	}
	if req.Memory > r.Memory {
		return false
	}
	if r.EphemeralStorage > 0 && req.EphemeralStorage > r.EphemeralStorage {
		return false
	}
	return true
}

func (r Resources) Add(o Resources) Resources {
	return Resources{CPU: r.CPU + o.CPU, Memory: r.Memory + o.Memory, EphemeralStorage: r.EphemeralStorage + o.EphemeralStorage}
}

func (r Resources) Sub(o Resources) Resources {
	return Resources{CPU: r.CPU - o.CPU, Memory: r.Memory - o.Memory, EphemeralStorage: r.EphemeralStorage - o.EphemeralStorage}
}

// SpecTag renders resources as a compact lowercase name fragment for
// instance names (k8s-safe): e.g. 2 CPU / 4Gi -> "2c4g", 500m CPU -> "500mc1g".
func (r Resources) SpecTag() string {
	cpu := "0c"
	if r.CPU > 0 {
		if r.CPU == float64(int(r.CPU)) {
			cpu = fmt.Sprintf("%dc", int(r.CPU))
		} else {
			cpu = fmt.Sprintf("%dmc", int(r.CPU*1000))
		}
	}
	mem := "0g"
	switch {
	case r.Memory >= Giga && r.Memory%Giga == 0:
		mem = fmt.Sprintf("%dg", r.Memory/Giga)
	case r.Memory >= Mega && r.Memory%Mega == 0:
		mem = fmt.Sprintf("%dm", r.Memory/Mega)
	case r.Memory > 0:
		mem = strconv.FormatInt(int64(r.Memory), 10)
	}
	return cpu + mem
}
