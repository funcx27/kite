// Package spec defines the DAG workflow specification: nodes, edges, the
// parameter schema and trigger configuration (design §4), plus the validation
// and template-resolution rules that guard saved definitions.
package spec

// Node types supported in M1 (design §5).
const (
	NodeBuildImage = "build-image"
	NodeDeployApp  = "deploy-app"
	NodeApproval   = "approval"
	NodeNotify     = "notify"
)

// Failure policies (design §4: onFailure stop 默认 / continue).
const (
	OnFailureStop     = "stop"
	OnFailureContinue = "continue"
)

// DAGSpec is the persisted shape of WorkflowRevision.Spec.
type DAGSpec struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Node is one DAG vertex; Props is the type-specific payload rendered by the
// template resolver at dispatch time (design §4).
type Node struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	// OnFailure defaults to stop when empty.
	OnFailure string `json:"onFailure,omitempty"`
	Props     Props  `json:"props,omitempty"`
	// Timeout caps node execution (duration string, e.g. "24h"); advisory in
	// M1 — enforced for task nodes by the engine, for approvals by the M3
	// patrol.
	Timeout string `json:"timeout,omitempty"`
}

// Props carries the raw, template-bearing node payload.
type Props map[string]any

// ApprovalProps is the approval-node payload (design §5.3). Assignees are the
// authorization: any one of them (or an admin) may decide (design §8).
type ApprovalProps struct {
	Assignees     []uint `json:"assignees"`
	Message       string `json:"message,omitempty"`
	Timeout       string `json:"timeout,omitempty"`       // enforced by the M3 patrol
	TimeoutAction string `json:"timeoutAction,omitempty"` // reject(默认) | approve
}

// BuildProps is the build-image payload (design §5.1). Exactly one of
// Git/HTTP sources must be set.
type BuildProps struct {
	Git               *GitSource        `json:"git,omitempty"`
	HTTP              *HTTPSource       `json:"http,omitempty"`
	Dockerfile        string            `json:"dockerfile,omitempty"`
	DockerfileContent string            `json:"dockerfileContent,omitempty"`
	Args              map[string]string `json:"args,omitempty"`
	Platforms         []string          `json:"platforms,omitempty"`
	OutputRef         string            `json:"outputRef,omitempty"`
	// GitCredentialRef names the credential center entry used for git fetch
	// auth; empty = anonymous.
	GitCredentialRef string `json:"gitCredentialRef,omitempty"`
	// RegistryCredentialRef names the credential center entry used for
	// registry push auth; empty = anonymous.
	RegistryCredentialRef string `json:"registryCredentialRef,omitempty"`
	Provider              string `json:"provider,omitempty"`
}

// GitSource points the build at a git checkout.
type GitSource struct {
	URL    string `json:"url"`
	Ref    string `json:"ref,omitempty"`
	Subdir string `json:"subdir,omitempty"`
}

// HTTPSource points the build at a tarball context.
type HTTPSource struct {
	URL string `json:"url"`
}

// DeployProps is the deploy-app payload (design §5.2).
type DeployProps struct {
	App     string `json:"app"`
	Cluster string `json:"cluster"`
	Image   string `json:"image,omitempty"` // empty = deploy the current config
	DryRun  bool   `json:"dryRun,omitempty"`
}

// NotifyProps is the notify payload (design §5.4).
type NotifyProps struct {
	Channel         string `json:"channel,omitempty"` // v1: webhook only
	URL             string `json:"url"`
	PayloadTemplate string `json:"payloadTemplate,omitempty"`
}

// Edge is one directed dependency: from must finish (succeeded, or failed
// with continue) before to may start.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Param types (design §4).
const (
	ParamString = "string"
	ParamNumber = "number"
	ParamBool   = "bool"
	ParamEnum   = "enum"
)

// ParamSpec describes one workflow parameter: manual triggers render a form,
// webhook/cron fills defaults.
type ParamSpec struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Default  any    `json:"default,omitempty"`
	Options  []any  `json:"options,omitempty"` // enum choices
}

// TriggerConfig selects the trigger surfaces of a workflow (design §7);
// MaxConcurrent caps runs per workflow (queued FIFO beyond it, default 3).
type TriggerConfig struct {
	// Manual is nil by default (absent in JSON), meaning enabled.
	Manual        *bool  `json:"manual,omitempty"`
	Webhook       bool   `json:"webhook,omitempty"`
	Cron          string `json:"cron,omitempty"` // cron expression; M3
	MaxConcurrent int    `json:"maxConcurrent,omitempty"`
}

// ManualEnabled reports whether the manual trigger surface is on; an absent
// flag means enabled (manual is the default surface).
func (t TriggerConfig) ManualEnabled() bool {
	return t.Manual == nil || *t.Manual
}

// DefaultMaxConcurrentRuns applies when TriggerConfig.MaxConcurrent is unset.
const DefaultMaxConcurrentRuns = 3
