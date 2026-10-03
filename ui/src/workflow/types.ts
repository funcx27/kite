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

export interface WorkflowListItem {
  id: number
  name: string
  namespace: string
  description: string
  currentRev: number
  // Deploy-app targets aggregated from the current revision's spec (present
  // only when the workflow has deploy-app nodes); used for list filtering.
  apps?: string[]
  clusters?: string[]
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
  namespace: string
  description: string
  spec: DAGSpec
  params: ParamSpec[]
  triggers: TriggerConfig
}

export interface UpdateWorkflowRequest {
  description: string
  spec: DAGSpec
  params: ParamSpec[]
  triggers: TriggerConfig
  expectedRev: number
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
  workflowNamespace: string
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
  gitUsername: string
  registryHosts: string[]
  registryAccounts: RegistryAccountSummary[]
  createdAt: string
  updatedAt: string
}

export interface GitCredentialPayload {
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

// ── Managed build providers (settings-created k8s build providers) ──

export interface BuildProvider {
  name: string
  cluster: string
  namespace: string
  cpu: number
  memory: string
}

export interface BuildProvidersResponse {
  providers: BuildProvider[]
  // Read-only provider names from the buildfleet config file; they cannot be
  // managed here and creation rejects their names.
  configProviders: string[]
}

export interface CreateBuildProviderRequest {
  name: string
  cluster: string
  namespace: string
  cpu?: number
  memory?: string
}
