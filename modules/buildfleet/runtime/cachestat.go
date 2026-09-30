package runtime

// ProjectStat is one project's cache usage inside a cache registry.
type ProjectStat struct {
	// Project is the repo path under buildfleet-cache (the pushed
	// image's repo path, or a flattened git path).
	Project   string
	Tags      int
	Layers    int   // digests referenced (layers + config)
	Exclusive int64 // bytes of digests only this project references
	Shared    int64 // bytes of digests also referenced by other projects
}

// CacheStatSnapshot describes one cache registry.
type CacheStatSnapshot struct {
	TotalBytes int64
	Projects   []ProjectStat
}
