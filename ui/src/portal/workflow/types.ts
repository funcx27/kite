// API shapes of the workflow module (modules/workflow/internal/api) and the
// credentials module (modules/credentials/internal/api), mirrored for the UI.

// ── Workflow definition ──

export type WorkflowNodeType =
  'build-image' | 'deploy-app' | 'approval' | 'notify'

export type OnFailurePolicy = 'stop' | 'continue'

// Node props stay loosely typed on the wire (backend spec.Props is a raw
// map); the per-type forms in property-drawer.tsx carry the concrete shapes.
export interface WorkflowNode {
  id: string
  type: WorkflowNodeType
  name: string
  onFailure?: OnFailurePolicy
  props?: Record<string, unknown>
  timeout?: string
}

export interface WorkflowEdge {
  from: string
  to: string
}

export interface DAGSpec {
  nodes: WorkflowNode[]
  edges: WorkflowEdge[]
}

export type ParamType = 'string' | 'number' | 'bool' | 'enum'

export interface ParamSpec {
  name: string
  type: ParamType
  required?: boolean
  default?: unknown
  options?: unknown[]
}

export interface TriggerConfig {
  manual?: boolean
  webhook?: boolean
  cron?: string
  maxConcurrent?: number
}

// The numeric ID is the definition identity; names are reusable display
// labels (same name may exist on several workflows).
export interface WorkflowListItem {
  id: number
  name: string
  description: string
  currentRev: number
  // Deploy-app targets of the current revision's spec, aggregated as
  // "space/name" composites (present only when the workflow has deploy-app
  // nodes); used for list filtering.
  apps?: string[]
  clusters?: string[]
  // Bound spaces: optional visibility metadata for list filtering (present
  // only when something is bound); deploy-referenced spaces derive from apps.
  spaces?: string[]
  createdAt: string
  updatedAt: string
}

export interface Workflow extends WorkflowListItem {
  spec?: DAGSpec
  params?: ParamSpec[]
  triggers?: TriggerConfig
}

export interface CreateWorkflowRequest {
  name: string
  description: string
  spec: DAGSpec
  params: ParamSpec[]
  triggers: TriggerConfig
  // Optional space bindings; omitted or empty leaves the workflow
  // platform-level.
  spaces?: string[]
}

export interface UpdateWorkflowRequest {
  description: string
  spec: DAGSpec
  params: ParamSpec[]
  triggers: TriggerConfig
  expectedRev: number
  // Renames the workflow when present (1–63 chars, any unicode); absent
  // keeps the stored name.
  name?: string
  // Replaces the space bindings when present (an empty array clears them).
  spaces?: string[]
}

export interface RevisionSummary {
  rev: number
  operatorId: number
  createdAt: string
}

// ── Runs ──

export type RunStatus =
  'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'

export type NodeRunStatus =
  | 'pending'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'skipped'
  | 'cancelled'
  | 'interrupted'

export interface RunSummary {
  id: number
  runNumber: number
  revisionRev: number
  status: RunStatus
  triggerType: 'manual' | 'webhook' | 'cron'
  triggeredBy?: number
  triggeredByName?: string
  triggerRef?: string
  params?: Record<string, unknown>
  startedAt?: string | null
  finishedAt?: string | null
  // Failed-node count for the warning badge on a succeeded run (continue
  // semantics: notify nodes or onFailure=continue).
  failedNodes?: number
}

export interface RunNodeView {
  nodeId: string
  type: WorkflowNodeType
  name: string
  status: NodeRunStatus
  inputs?: Record<string, unknown>
  outputs?: Record<string, unknown>
  error?: string
  logTail?: string
  attempt?: number
  startedAt?: string | null
  finishedAt?: string | null
}

export interface RunDetail {
  id: number
  runNumber: number
  workflow: string
  revisionRev: number
  status: RunStatus
  triggerType: RunSummary['triggerType']
  triggeredBy?: number
  triggeredByName?: string
  triggerRef?: string
  params?: Record<string, unknown>
  startedAt?: string | null
  finishedAt?: string | null
  graph: DAGSpec
  nodes: RunNodeView[]
}

export interface TriggerRunResponse {
  id: number
  runNumber: number
  status: RunStatus
  revisionRev: number
}

// ── SSE run events (GET /runs/:id/events, design §10) ──

export type RunStreamEvent =
  | { kind: 'run'; status: RunStatus }
  | { kind: 'node'; nodeId: string; status: NodeRunStatus }
  | { kind: 'log'; nodeId: string; data: string }
  | { kind: 'done'; status: RunStatus }
  | { kind: 'error'; error: string }

// ── Credentials (shape summaries only; payloads are write-only) ──

export interface RegistryAccountSummary {
  host: string
  username: string
}

export interface CredentialSummary {
  name: string
  description: string
  hasGit: boolean
  gitHost: string
  gitUsername: string
  registryHosts: string[]
  registryAccounts: RegistryAccountSummary[]
  createdAt: string
  updatedAt: string
}

export interface GitCredentialPayload {
  host?: string
  username: string
  password: string
}

export interface RegistryCredentialPayload {
  host: string
  username: string
  password: string
}

export interface CredentialPayload {
  git?: GitCredentialPayload
  registry?: RegistryCredentialPayload[]
}

export interface CreateCredentialRequest {
  name: string
  description: string
  payload: CredentialPayload
}

// ── Build settings (namespace build-cluster defaults) ──

export interface BuildSetting {
  // '' = the global default row.
  namespace: string
  provider: string
}

export interface BuildSettingsResponse {
  settings: BuildSetting[]
  providers: string[]
}

// ── Managed build providers (settings-created k8s / docker build providers) ──

export interface BuildProviderCache {
  enabled: boolean
  image: string
  storageClass: string
  // Cache volume size, e.g. "20Gi".
  size: string
  // Cron expression for registry garbage collection.
  gcSchedule: string
}

// Docker mutual-TLS material; file paths on the kite host.
export interface BuildProviderTls {
  ca: string
  cert: string
  key: string
  insecureSkipVerify: boolean
}

export interface BuildProvider {
  // 'k8s' (managed cluster) or 'docker' (the kite host's daemon).
  type: 'k8s' | 'docker'
  name: string
  // false = unregistered from the build fleet, row kept for re-enabling.
  enabled: boolean
  // k8s only: the cluster the provider builds on and the namespace that
  // receives the buildkitd pods.
  cluster: string
  namespace: string
  // Docker only: daemon endpoint ('' = the host's default daemon).
  address: string
  tls: BuildProviderTls | null
  // Fleet capacity caps: cpu cores and a memory quantity like "16Gi".
  cpu: number
  memory: string
  // Runtime profile name; '' = the default profile.
  runtime: string
  // Buildkitd pod readiness wait in seconds; 0 = module default. k8s only.
  readyTimeoutSeconds: number
  // Per-instance build pod shape (cpu cores / memory Gi). k8s only.
  podCpu: number
  podMemoryGi: number
  cache: BuildProviderCache | null
  // Node names the buildkitd pods are pinned to via node affinity. k8s
  // only; empty = any schedulable node.
  nodes: string[]
}

// Effective built-in defaults the UI displays in the create dialog instead
// of empty fields.
export interface BuildProviderDefaults {
  readyTimeoutSeconds: number
  podCpu: number
  podMemoryGi: number
  cacheImage: string
  cacheSize: string
  cacheGcSchedule: string
  dockerCpu: number
  dockerMemory: string
}

export interface BuildProvidersResponse {
  providers: BuildProvider[]
  // Read-only provider names from the buildfleet config file; they cannot be
  // managed here and creation rejects their names.
  configProviders: string[]
  // Configured runtime profile names, usable as the provider runtime choice.
  runtimes: string[]
  defaults: BuildProviderDefaults
}

// Provider fields shared by create and update (everything but the identity
// and target). Optional numbers default server-side: 0 keeps the built-in
// default, an empty runtime selects the default profile, null cache disables
// the in-cluster registry cache.
export interface BuildProviderFields {
  // Runtime kind; immutable after creation.
  type: 'k8s' | 'docker'
  runtime?: string
  readyTimeoutSeconds?: number
  podCpu?: number
  podMemoryGi?: number
  cache?: BuildProviderCache | null
  // k8s only: node names pinning the buildkitd pods. PUT semantics — the
  // full replacement, an empty array clears the pin.
  nodes?: string[]
}

export interface CreateBuildProviderRequest extends BuildProviderFields {
  name: string
  type: 'k8s' | 'docker'
  cluster?: string
  namespace?: string
  address?: string
  tls?: BuildProviderTls | null
  cpu?: number
  memory?: string
}

// The provider name and type are immutable: changing either means recreating.
export interface UpdateBuildProviderRequest extends BuildProviderFields {
  cluster?: string
  namespace?: string
  address?: string
  tls?: BuildProviderTls | null
  cpu?: number
  memory?: string
}

// ── Build runtime settings (default runtime profile wiring) ──

export interface RuntimeHostEntry {
  ip: string
  names: string[]
}

export interface RuntimeSettings {
  // Buildkit images of the two build modes; '' = built-in default.
  rootfulImage: string
  rootlessImage: string
  hosts: RuntimeHostEntry[]
  insecureRegistries: string[]
  // Source registry host → internal mirror sources.
  mirrors: Record<string, string[]>
}
