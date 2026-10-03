// Types for the application module API (modules/application/internal/api),
// aligned with design docs/demo/design.md §7.

// ── Wizard spec (render.AppSpec) ──

export interface ApplicationEnvVar {
  name: string
  value?: string
}

export interface ApplicationResourceSpec {
  cpu?: string
  memory?: string
}

export interface ApplicationWorkloadSpec {
  type: string // deployment | statefulset
  image: string
  replicas?: number
  env?: ApplicationEnvVar[]
  requests?: ApplicationResourceSpec
  limits?: ApplicationResourceSpec
}

export interface ApplicationPortSpec {
  port: number
  type?: string // clusterip | nodeport | loadbalancer
  host?: string
  path?: string
  name?: string
}

export interface ApplicationConfigFile {
  name: string
  mountPath: string
  content: string
  secret: boolean
}

export interface ApplicationSpec {
  workload: ApplicationWorkloadSpec
  mountPoints?: string[]
  configFiles?: ApplicationConfigFile[]
  ports?: ApplicationPortSpec[]
  ingressClass?: string
  extraYaml?: string
}

// ── Manifests ──

export interface ApplicationManifest {
  apiVersion: string
  kind: string
  metadata?: {
    name: string
    namespace?: string
    labels?: Record<string, string>
    [key: string]: unknown
  }
  [key: string]: unknown
}

// ── Applications (apps_api.go) ──

export type ApplicationStatusSummary =
  'synced' | 'drift' | 'conflict' | 'merged' | 'undeployed' | 'error' | ''

export interface ApplicationConfigSummary {
  id: number
  cluster: string
  namespace: string
  statusSummary: ApplicationStatusSummary
  statusCheckedAt: string | null
  lastDeployedAt: string | null
}

export interface Application {
  id: number
  name: string
  namespace: string
  description: string
  spec?: ApplicationSpec
  createdAt: string
  updatedAt: string
  configs: ApplicationConfigSummary[]
}

export interface ApplicationListResponse {
  applications: Application[]
  total: number
}

export interface CreateApplicationRequest {
  name: string
  namespace?: string
  description?: string
  spec?: ApplicationSpec
}

export interface UpdateApplicationRequest {
  description?: string
  spec?: ApplicationSpec
}

// ── Per-cluster config (configs_api.go) ──

export interface ApplicationConfig {
  id: number
  appId: number
  cluster: string
  namespace: string
  manifests: ApplicationManifest[]
  spec?: ApplicationSpec
  lastApplied: ApplicationManifest[]
  /** Pull credential whose registry entries kite upserts as a managed
   *  image pull secret on every publish; empty disables it. */
  credentialRef: string
  statusSummary: ApplicationStatusSummary
  statusCheckedAt: string | null
  overriddenPaths: string[]
  lastDeployedAt: string | null
  lastSyncedAt: string | null
  latestRev: number
}

export interface CreateApplicationConfigRequest {
  cluster: string
  copyFrom?: string
}

export interface UpdateApplicationConfigRequest {
  spec?: ApplicationSpec
  manifests?: ApplicationManifest[]
  expectedRev: number
}

// Wizard edits handed over to the deploy dialog: persisted as a revision and
// applied by the same confirm-publish request — never saved on their own.
export interface ApplicationPendingChange {
  spec?: ApplicationSpec
  manifests?: ApplicationManifest[]
  expectedRev: number
}

// ── Deploy (deploy_api.go) ──

export interface ApplicationDeployRequest {
  dryRun?: boolean
  image?: string
  // Pending config changes (see ApplicationPendingChange): persisted and
  // applied by this one request; dryRun only validates them in memory.
  spec?: ApplicationSpec
  manifests?: ApplicationManifest[]
  expectedRev?: number
  /** Sets (empty string clears) the config's pull credential; omitted leaves
   *  the stored value untouched. */
  credentialRef?: string
}

export interface ApplicationDeployDocResult {
  kind: string
  namespace?: string
  name: string
  action: 'create' | 'update'
  success: boolean
  error?: string
}

// One changed document's redacted before/after pair for the YAML diff popup.
export interface ApplicationDeployDocPreview {
  kind: string
  name: string
  action: 'create' | 'update'
  old?: ApplicationManifest
  new?: ApplicationManifest
}

export interface ApplicationDocDiff {
  kind: string
  namespace?: string
  name: string
  missingIn?: 'a' | 'b'
  paths?: string[]
  truncated?: boolean
}

export interface ApplicationDeployResponse {
  success: boolean
  dryRun: boolean
  results: ApplicationDeployDocResult[]
  anchored: boolean
  anchorError?: string
  drift?: ApplicationDocDiff[]
  pending?: ApplicationDocDiff[]
  change?: ApplicationDocDiff[]
  preview?: ApplicationDeployDocPreview[]
  latestRev?: number
}

// 409 body of a deploy rejected by the conflict gate (deploy_api.go).
export interface ApplicationConflict {
  kind: string
  namespace?: string
  name: string
  paths: string[]
}

// ── Merge (merge_api.go, syncops/merge.go) ──

export const MERGE_DOC_ROOT_PATH = '$doc'

export type ApplicationMergeChoice = 'ours' | 'theirs'

export interface ApplicationMergeCollision {
  path: string
  base?: unknown
  ours?: unknown
  theirs?: unknown
  chosen?: ApplicationMergeChoice
}

export interface ApplicationMergeAutoItem {
  path: string
  /** Value in the baseline before the cluster-side change (omitted when absent). */
  base?: unknown
  /** Adopted cluster-side value (omitted when the cluster deleted it). */
  theirs?: unknown
}

export interface ApplicationMergeDocReport {
  kind: string
  namespace?: string
  name: string
  onlyIn?: 'ours' | 'theirs'
  dropped?: boolean
  autoAdopted?: ApplicationMergeAutoItem[]
  overridden?: string[]
  collisions?: ApplicationMergeCollision[]
}

export interface ApplicationMergePreview {
  merged: ApplicationManifest[]
  docs: ApplicationMergeDocReport[]
  /** False when confirming ends in a synced config (pure adoption): no publish follows. */
  publishRequired?: boolean
}

export interface ApplicationMergeRequest {
  // Keys are scoped paths "<docKey>:<path>", values are ours|theirs.
  choices?: Record<string, ApplicationMergeChoice>
  // Scoped paths of auto-adopted cluster changes reverted to the baseline;
  // persisted as OverriddenPaths (知情覆盖).
  rollbacks?: string[]
}

export interface ApplicationMergeResponse {
  latestRev: number
  overriddenPaths: string[]
  /** False when the merged set already matches the cluster (pure adoption): re-anchored, no publish needed. */
  publishRequired?: boolean
}

// ── Sync / offline (sync_api.go, offline_api.go) ──

export interface ApplicationSyncResponse {
  success: boolean
  adopted: string[]
  kept: string[]
  skipped: string[]
  latestRev: number
}

export interface ApplicationOfflineRequest {
  deleteConfig?: boolean
}

export interface ApplicationOfflineFailure {
  doc: string
  error: string
}

export interface ApplicationOfflineResponse {
  success: boolean
  deleted: string[]
  missing: string[]
  failed?: ApplicationOfflineFailure[]
  configDeleted: boolean
  appUndeployed: boolean
}

// ── Status report (status_api.go) ──

export interface ApplicationStatusReport {
  status: ApplicationStatusSummary
  action?: string
  checkedAt: string
  drift: ApplicationDocDiff[]
  pending: ApplicationDocDiff[]
  error?: string
}

// ── Revisions (revisions_api.go) ──

export interface ApplicationRevisionSummary {
  rev: number
  source: string
  /** The revision a derived one was built from (rollback: the source rev); 0 when n/a. */
  refRev?: number
  operatorId: number
  /** Resolved operator display name; absent when the user no longer exists. */
  operatorName?: string
  createdAt: string
  redacted: boolean
}

export interface ApplicationRevisionListResponse {
  revisions: ApplicationRevisionSummary[]
  total: number
}

export interface ApplicationRevisionDetail {
  rev: number
  source: string
  operatorId: number
  createdAt: string
  manifests: ApplicationManifest[]
}

export interface ApplicationRollbackRequest {
  rev: number
  // One-step rollback: persist the rollback revision and publish it through
  // the conflict-gated pipeline in the same request.
  deploy?: boolean
}

export interface ApplicationRollbackResponse {
  latestRev: number
  rolledBackTo: number
  success?: boolean
  results?: ApplicationDeployDocResult[]
}

// ── Runtime view (runtime/view.go) ──

export interface ApplicationRuntimeSection {
  unauthorized: boolean
}

export interface ApplicationRuntimeCondition {
  type: string
  status: string
  reason?: string
  message?: string
  lastUpdateTime?: string
  lastTransitionTime?: string
}

export interface ApplicationRuntimeResourceValues {
  cpu?: string
  memory?: string
}

export interface ApplicationRuntimeWorkload extends ApplicationRuntimeSection {
  kind?: string
  name?: string
  namespace?: string
  replicas: number
  ready: number
  conditions?: ApplicationRuntimeCondition[]
  /** pod template requests/limits, summed across containers */
  requests?: ApplicationRuntimeResourceValues
  limits?: ApplicationRuntimeResourceValues
  /** pod template container images */
  images?: string[]
}

export interface ApplicationRuntimePod {
  name: string
  phase?: string
  /** kubectl-style display status (waiting/terminated reason over the phase, e.g. ImagePullBackOff) */
  status?: string
  /** spec.containers names, for the terminal / logs container pickers */
  containers?: string[]
  node?: string
  ip?: string
  images?: string[]
  startTime?: string
  restarts: number
  cpu?: number
  memory?: number
  /** pod spec requests/limits, summed across containers */
  requests?: ApplicationRuntimeResourceValues
  limits?: ApplicationRuntimeResourceValues
}

export interface ApplicationRuntimePods extends ApplicationRuntimeSection {
  metricsAvailable: boolean
  items: ApplicationRuntimePod[]
}

export interface ApplicationRuntimeServicePort {
  name?: string
  port: number
  targetPort?: string
  nodePort?: number
  protocol?: string
}

export interface ApplicationRuntimeService {
  name: string
  type?: string
  clusterIP?: string
  ports?: ApplicationRuntimeServicePort[]
}

export interface ApplicationRuntimeServices extends ApplicationRuntimeSection {
  items: ApplicationRuntimeService[]
}

export interface ApplicationRuntimeIngressPath {
  path?: string
  pathType?: string
  service?: string
  port?: string
}

export interface ApplicationRuntimeIngressRule {
  host?: string
  paths?: ApplicationRuntimeIngressPath[]
}

export interface ApplicationRuntimeIngress {
  name: string
  rules?: ApplicationRuntimeIngressRule[]
  lbAddresses?: string[]
}

export interface ApplicationRuntimeIngresses extends ApplicationRuntimeSection {
  items: ApplicationRuntimeIngress[]
}

export interface ApplicationRuntimeEvent {
  involvedKind?: string
  involvedName?: string
  reason?: string
  message?: string
  count?: number
  lastTimestamp?: string
}

export interface ApplicationRuntimeEvents extends ApplicationRuntimeSection {
  items: ApplicationRuntimeEvent[]
}

export interface ApplicationRuntimeView {
  workload: ApplicationRuntimeWorkload
  pods: ApplicationRuntimePods
  services: ApplicationRuntimeServices
  ingresses: ApplicationRuntimeIngresses
  events: ApplicationRuntimeEvents
}

// ── Preview (preview_api.go) ──

export interface ApplicationPreviewRequest {
  name: string
  namespace?: string
  spec: ApplicationSpec
}

export interface ApplicationPreviewResponse {
  manifests: ApplicationManifest[]
  yaml: string
}
