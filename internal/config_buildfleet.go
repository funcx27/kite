package internal

import (
	"gopkg.in/yaml.v3"
)

// The buildfleet module's config subtree, kept out of KiteConfig so that file
// stays byte-aligned with upstream. captureBuildFleetNode re-parses the
// already-expanded config text (config files are small; one extra Unmarshal
// per read is negligible against keeping the shared type untouched).

// buildFleetNode is the raw buildfleet config subtree from the most recent
// config file read; nil when the section is absent or null (module disabled).
// The host builds the buildfleet runtime from it once at startup; config file
// reloads replace the node but do not re-provision the module.
var buildFleetNode *yaml.Node

// BuildFleetConfig returns the raw buildfleet config subtree, or nil when the
// buildfleet section is missing or empty. Values are already ${ENV_VAR}
// expanded, matching the rest of the config file.
func BuildFleetConfig() *yaml.Node {
	return buildFleetNode
}

// captureBuildFleetNode extracts the buildfleet subtree from the expanded
// config text. Hook: called from readConfigFile. The field is a value Node
// because yaml.v3 v3.0.1 leaves *yaml.Node struct fields zeroed after
// Unmarshal (verified: value decodes, pointer stays Kind 0). The node is
// never persisted to the DB and never listed in AppliedSections.
func captureBuildFleetNode(expanded string) {
	var probe struct {
		BuildFleet yaml.Node `yaml:"buildfleet"`
	}
	if err := yaml.Unmarshal([]byte(expanded), &probe); err != nil {
		// readConfigFile has already rejected unparseable text; a failure
		// here would mean the same bytes parse differently, which cannot
		// happen. Treat as disabled rather than failing startup.
		buildFleetNode = nil
		return
	}
	buildFleetNode = enabledNode(&probe.BuildFleet)
}

// enabledNode normalizes "section absent" (zero Node), "section null"
// (buildfleet:) and "empty mapping" (buildfleet: {}) to nil, so all three
// mean the module is disabled. An empty mapping would otherwise fall through
// to the module's built-in defaults and silently start a build service.
func enabledNode(n *yaml.Node) *yaml.Node {
	if n == nil || n.Kind == 0 ||
		(n.Kind == yaml.ScalarNode && n.Tag == "!!null") ||
		(n.Kind == yaml.MappingNode && len(n.Content) == 0) {
		return nil
	}
	return n
}
