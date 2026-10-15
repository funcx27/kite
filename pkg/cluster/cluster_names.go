package cluster

import "sort"

// ClusterNames returns the sorted names of every cluster the manager knows
// about, including ones whose client failed to load (those resolve to errors
// for callers that go through GetClientSet).
func (cm *ClusterManager) ClusterNames() []string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	names := make([]string, 0, len(cm.clusters)+len(cm.errors))
	for name := range cm.clusters {
		names = append(names, name)
	}
	for name := range cm.errors {
		if _, ok := cm.clusters[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
