// Package exec defines the executor request/result types the workflow module
// exchanges with its injected executors (design §5): image builds, app
// deploys and webhook notifications. It is a public package so the host
// adapter can implement the Deps executor methods, while the module entry,
// the engine and the HTTP layer share the same types without import cycles.
package exec

import "encoding/json"

// BuildRequest asks the build executor to produce one image (design §5.1).
// Exactly one of Git/HTTP must be set; credentials are resolved executor-side
// from GitCredentialRef/RegistryCredentialRef — the module never sees secret
// material.
type BuildRequest struct {
	Git               *GitSource        `json:"git,omitempty"`
	HTTP              *HTTPSource       `json:"http,omitempty"`
	Dockerfile        string            `json:"dockerfile,omitempty"`
	DockerfileContent string            `json:"dockerfileContent,omitempty"`
	Args              map[string]string `json:"args,omitempty"`
	Platforms         []string          `json:"platforms,omitempty"`
	OutputRef         string            `json:"outputRef,omitempty"`
	// GitCredentialRef references the credential center entry used for git
	// fetch auth; empty = anonymous.
	GitCredentialRef string `json:"gitCredentialRef,omitempty"`
	// RegistryCredentialRef references the credential center entry used for
	// registry push auth; empty = anonymous.
	RegistryCredentialRef string `json:"registryCredentialRef,omitempty"`
	// Provider pins the build runtime; empty = any.
	Provider string `json:"provider,omitempty"`
}

// BuildResult reports the produced image (design §5.1 Outputs).
type BuildResult struct {
	ImageRef string `json:"imageRef"`
	Digest   string `json:"digest"`
	// Commit is the actually-resolved git commit of the build; empty when the
	// source was not git or the commit is unknown.
	Commit string `json:"commit,omitempty"`
}

// GitSource points the build at a git checkout.
type GitSource struct {
	URL string `json:"url"`
	Ref string `json:"ref,omitempty"`
	// Mode selects the fetch side: "server" (default, daemon-side fetch) |
	// "runtime" (buildkitd fetches).
	Mode   string `json:"mode,omitempty"`
	Subdir string `json:"subdir,omitempty"`
}

// HTTPSource points the build at a tarball context URL.
type HTTPSource struct {
	URL string `json:"url"`
}

// DeployRequest asks the deploy executor to publish one image to an
// application (design §5.2). OperatorID is the run's RBAC identity — the
// deploy executor enforces per-document permissions with it (design §7).
type DeployRequest struct {
	// Space is the owning space of App; with App it names the target.
	Space   string `json:"space"`
	App     string `json:"app"`
	Cluster string `json:"cluster"`
	Image   string `json:"image,omitempty"` // empty = deploy the current config
	// RegistryCredentialRef references the credential center entry whose
	// managed pull secret is attached to the publish; empty = keep the
	// application config's current credential.
	RegistryCredentialRef string `json:"registryCredentialRef,omitempty"`
	DryRun                bool   `json:"dryRun,omitempty"`
	OperatorID            uint   `json:"operatorId"`
}

// DeployResult reports the deploy outcome (design §5.2 Outputs): the produced
// application revision and the change preview documents.
type DeployResult struct {
	Revision int             `json:"revision"`
	Preview  json.RawMessage `json:"preview,omitempty"`
}

// NotifyRequest asks the notify executor to deliver one webhook payload
// (design §5.4). Delivery is best-effort: failures fail the node but never
// block the pipeline (notify nodes run with continue semantics).
type NotifyRequest struct {
	URL     string `json:"url"`
	Payload []byte `json:"payload"`
}
