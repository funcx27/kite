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
  imagePullPolicy?: string
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
  /** Certificate name rendered as an Ingress TLS block + managed kubernetes.io/tls Secret. */
  tlsCertificate?: string
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
  'synced' | 'drift' | 'pending' | 'undeployed' | 'error' | ''

export interface ApplicationConfigSummary {
  id: number
  cluster: string
  namespace: string
  statusSummary: ApplicationStatusSummary
  statusCheckedAt: string | null
  lastDeployedAt: string | null
  // Live workload replica snapshot from the last persisted status check;
  // null when the check did not reach the workload (undeployed / unchecked).
  readyReplicas?: number | null
  replicas?: number | null
  /** The config's namespace lost the space label (space unlinked on that
   *  cluster): the config stays visible but carries the unlinked marker. */
  spaceUnlinked?: boolean
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
  /** Present when the creation request carried a cluster: the chain
   *  create-app + create-config + first publish ran; same shape as the
   *  deploy endpoint's response. */
  deploy?: ApplicationDeployResult
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
  /** When set, the app is created, its cluster config rendered from the
   *  spec, and the first publish executed in one chain. */
  cluster?: string
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
  /** Pull credential whose registry entries kite upserts as a managed
   *  image pull secret on every publish; empty disables it. */
  credentialRef: string
  statusSummary: ApplicationStatusSummary
  statusCheckedAt: string | null
  lastDeployedAt: string | null
  lastSyncedAt: string | null
  latestRev: number
  /** Revision the last successful publish pushed to the cluster; save-only
   *  edits never move it. 0 = never published (or predates the marker). */
  appliedRev: number
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
  /** Create mode only (target config does not exist yet): deep-copy this
   *  cluster's manifests as the base instead of rendering App.Spec. */
  copyFrom?: string
  /** Dry-run preview token echoed back on the real deploy: the server
   *  re-computes the diff and answers 409 when the cluster moved on. */
  previewToken?: string
  /** Persist the pending set as a revision without applying it to the
   *  cluster (仅保存): the config then shows as pending until published. */
  saveOnly?: boolean
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
  /** Set when the document exists on one side only: the side it is missing
   *  from ("cluster" = DB-only, pending publish; "manifests" = cluster-only). */
  missingIn?: 'manifests' | 'cluster'
  paths?: string[]
  truncated?: boolean
}

export interface ApplicationDeployResponse {
  success: boolean
  dryRun: boolean
  /** True on a save-only response: nothing was applied to the cluster. */
  saveOnly?: boolean
  results: ApplicationDeployDocResult[]
  anchored: boolean
  anchorError?: string
  change?: ApplicationDocDiff[]
  preview?: ApplicationDeployDocPreview[]
  /** Present on dry-run responses; echo it back on the real deploy. */
  previewToken?: string
  latestRev?: number
}

// The deploy payload embedded in the create-application response when the
// request carried a cluster (structurally identical to ApplicationDeployResponse).
export type ApplicationDeployResult = ApplicationDeployResponse

// ── Offline (offline_api.go) ──

export interface ApplicationOfflineFailure {
  doc: string
  error: string
}

export interface ApplicationOfflineResponse {
  success: boolean
  deleted: string[]
  missing: string[]
  failed?: ApplicationOfflineFailure[]
  /** Set when the cluster no longer exists: nothing was deleted in-cluster. */
  clusterGone?: boolean
  /** Always true: offline deletes the cluster objects and the config row. */
  configDeleted: boolean
  appUndeployed: boolean
}

// ── Status (status_api.go) ──

// One status document: the change set plus the redacted normalized live and
// manifest views the diff paths refer to. A document missing on one side
// omits that side's view and names it in missingIn.
export interface ApplicationStatusDoc extends ApplicationDocDiff {
  live?: ApplicationManifest
  manifest?: ApplicationManifest
  /** Backend-generated notice for a synthetic entry with no diff content
   * (certificate rotation findings). */
  note?: string
}

export interface ApplicationStatusReport {
  status: ApplicationStatusSummary
  docs: ApplicationStatusDoc[]
  /** Stable hash of the diff behind this report; resolve requires it back. */
  previewToken?: string
  checkedAt: string
  error?: string
}

// ── Resolve (resolve_api.go) ──

// One document's selected drift paths: the live subtree at each path is
// adopted into the stored manifests. Only documents present on both sides
// are accepted — one-sided documents are handled by sync / publish.
export interface ApplicationResolveAdoption {
  kind: string
  namespace?: string
  name: string
  paths: string[]
}

export interface ApplicationResolveRequest {
  expectedRev: number
  previewToken: string
  adoptions: ApplicationResolveAdoption[]
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
  // the same pipeline as a deploy.
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

// ── Space quotas (spaces_api.go) ──

// One cluster's quota of the upsert body; cpu is core count ("4", "0.5"),
// memory ends in Gi ("8Gi").
export interface SpaceQuotaInput {
  cluster: string
  cpu: string
  memory: string
}

export interface SpaceQuotaFailure {
  cluster: string
  error: string
}

export interface SpaceQuotaUpsertResponse {
  succeeded: string[]
  failed: SpaceQuotaFailure[]
}

// One cluster's view of a space: hard limits plus quota-observed usage as
// quantity strings, or one of the degraded markers (unconfigured /
// unauthorized / error).
export interface SpaceQuotaStatus {
  cluster: string
  cpu?: string
  memory?: string
  usedCpu?: string
  usedMemory?: string
  unconfigured?: boolean
  unauthorized?: boolean
  error?: string
}

export interface SpaceQuotaListResponse {
  quotas: SpaceQuotaStatus[]
}

// ── Certificates (pkg/certificate) ──

// Metadata view shared by the user-side list (wizard picker) and the admin
// list; the PEM chain and key are never part of it.
export interface CertificateMeta {
  id: number
  name: string
  description: string
  commonName: string
  dnsNames: string[]
  issuer: string
  notBefore: string
  notAfter: string
  fingerprint: string
  createdAt: string
}

// Admin detail/edit round trip: adds the chain so the edit form can prefill
// it; the key is write-only by design.
export interface CertificateDetail extends CertificateMeta {
  certPem: string
  updatedAt: string
}

export interface CertificateListResponse {
  certificates: CertificateMeta[]
}

export interface CreateCertificateRequest {
  name: string
  description?: string
  certPem: string
  keyPem: string
}

// Partial edit: certPem omitted = keep the stored chain; a changed certPem
// requires keyPem; a lone keyPem must match the stored chain.
export interface UpdateCertificateRequest {
  description?: string
  certPem?: string
  keyPem?: string
}
