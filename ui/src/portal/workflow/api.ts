// Standalone fetch client for the workflow module (design §11: the module
// ships its own API surface, it does not touch the shared lib/api-client).
// Every call sends cookies and the current-cluster header, mirroring what the
// host client does for module routes mounted under /api/v1.
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'

import {
  appendCurrentClusterHeader,
  appendCurrentClusterParam,
} from '@/lib/current-cluster'
import { withSubPath } from '@/lib/subpath'

import type {
  BuildProvider,
  BuildProvidersResponse,
  BuildSetting,
  BuildSettingsResponse,
  CreateBuildProviderRequest,
  CreateCredentialRequest,
  CreateWorkflowRequest,
  CredentialPayload,
  CredentialSummary,
  DefaultRegistrySetting,
  ParamSpec,
  RevisionSummary,
  RunDetail,
  RunSummary,
  RuntimeSettings,
  TriggerConfig,
  TriggerRunResponse,
  UpdateBuildProviderRequest,
  UpdateWorkflowRequest,
  Workflow,
  WorkflowListItem,
} from './types'

// ApiError preserves status + body so pages can surface server validation
// problems (400 spec errors, 409 expectedRev / running-runs conflicts).
export class WorkflowApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'WorkflowApiError'
    this.status = status
  }
}

async function workflowRequest<T>(
  path: string,
  init?: RequestInit
): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(init?.headers as Record<string, string>),
  }
  appendCurrentClusterHeader(headers)
  const response = await fetch(withSubPath(`/api/v1${path}`), {
    credentials: 'include',
    ...init,
    headers,
  })
  const body: unknown = await response.json().catch(() => ({}))
  if (!response.ok) {
    const payload = (body ?? {}) as { error?: unknown }
    const message =
      typeof payload.error === 'string'
        ? payload.error
        : `HTTP ${response.status}`
    throw new WorkflowApiError(response.status, message)
  }
  return body as T
}

// ── Workflow definitions ──

export const fetchWorkflows = () =>
  workflowRequest<{ workflows: WorkflowListItem[]; total: number }>(
    '/workflows'
  )

// The workflow identity is the numeric ID (names are reusable display
// labels), so every per-workflow path addresses it by /workflows/:id.
export const fetchWorkflow = (id: number) =>
  workflowRequest<Workflow>(`/workflows/${id}`)

export const createWorkflow = (req: CreateWorkflowRequest) =>
  workflowRequest<Workflow>('/workflows', {
    method: 'POST',
    body: JSON.stringify(req),
  })

export const updateWorkflow = (id: number, req: UpdateWorkflowRequest) =>
  workflowRequest<Workflow>(`/workflows/${id}`, {
    method: 'PUT',
    body: JSON.stringify(req),
  })

export const deleteWorkflow = (id: number) =>
  workflowRequest<{ message: string }>(`/workflows/${id}`, {
    method: 'DELETE',
  })

export const fetchWorkflowRevisions = (id: number) =>
  workflowRequest<{ revisions: RevisionSummary[] }>(
    `/workflows/${id}/revisions`
  )

export const fetchWebhookToken = (id: number) =>
  workflowRequest<{ token: string }>(`/workflows/${id}/webhook-token`)

export const resetWebhookToken = (id: number) =>
  workflowRequest<{ token: string }>(`/workflows/${id}/webhook-token/reset`, {
    method: 'POST',
  })

// ── Runs ──

export const triggerRun = (id: number, params: Record<string, unknown>) =>
  workflowRequest<TriggerRunResponse>(`/workflows/${id}/runs`, {
    method: 'POST',
    body: JSON.stringify({ params }),
  })

export const fetchRuns = (id: number, limit = 50) =>
  workflowRequest<{ runs: RunSummary[]; total: number }>(
    `/workflows/${id}/runs?limit=${limit}`
  )

export const fetchRun = (runId: number | string) =>
  workflowRequest<RunDetail>(`/runs/${runId}`)

export const cancelRun = (runId: number | string) =>
  workflowRequest<{ message: string }>(`/runs/${runId}/cancel`, {
    method: 'POST',
  })

export const retryRun = (runId: number | string) =>
  workflowRequest<{ message: string }>(`/runs/${runId}/retry`, {
    method: 'POST',
  })

export const decideApproval = (
  runId: number | string,
  nodeId: string,
  approve: boolean,
  comment: string
) =>
  workflowRequest<{ message: string }>(
    `/runs/${runId}/nodes/${encodeURIComponent(nodeId)}/${approve ? 'approve' : 'reject'}`,
    { method: 'POST', body: JSON.stringify({ comment }) }
  )

// SSE stream URL for GET /runs/:id/events. EventSource cannot send the
// cluster header, so the cluster rides as the query param the host
// ClusterMiddleware also accepts.
export function runEventsUrl(runId: number | string): string {
  const params = new URLSearchParams()
  appendCurrentClusterParam(params)
  const query = params.toString()
  return withSubPath(`/api/v1/runs/${runId}/events${query ? `?${query}` : ''}`)
}

// ── Credentials ──

export const fetchCredentials = () =>
  workflowRequest<{ credentials: CredentialSummary[]; total: number }>(
    '/credentials'
  )

// ── User directory ──

export interface UserDirectoryEntry {
  id: number
  username: string
}

export const fetchUserDirectory = () =>
  workflowRequest<{ users: UserDirectoryEntry[] }>('/users/directory')

export const createCredential = (req: CreateCredentialRequest) =>
  workflowRequest<CredentialSummary>('/credentials', {
    method: 'POST',
    body: JSON.stringify(req),
  })

// PUT replaces description + payload in one server-side request (the module's
// Update handler) — used for "edit", since secrets are write-only and must be
// re-entered wholesale.
export const updateCredential = (
  name: string,
  req: { description: string; payload: CredentialPayload }
) =>
  workflowRequest<CredentialSummary>(
    `/credentials/${encodeURIComponent(name)}`,
    {
      method: 'PUT',
      body: JSON.stringify(req),
    }
  )

export const deleteCredential = (name: string) =>
  workflowRequest<{ message: string }>(
    `/credentials/${encodeURIComponent(name)}`,
    {
      method: 'DELETE',
    }
  )

// ── Default registry pointer ──
// The path segment starts with an underscore, which credential names
// (DNS-1123 labels) can never do, so it cannot collide with /credentials/:name.
export const fetchDefaultRegistry = () =>
  workflowRequest<DefaultRegistrySetting>('/credentials/_default-registry')

// PUT validates server-side that the credential exists and carries the host
// in its registry payload before storing the pointer.
export const setDefaultRegistry = (req: { credential: string; host: string }) =>
  workflowRequest<DefaultRegistrySetting>('/credentials/_default-registry', {
    method: 'PUT',
    body: JSON.stringify(req),
  })

// ── Build settings ──

export const fetchBuildSettings = () =>
  workflowRequest<BuildSettingsResponse>('/build-settings')

// The global default row (namespace '') is reachable at the bare path.
const buildSettingPath = (namespace: string) =>
  namespace === ''
    ? '/build-settings'
    : `/build-settings/${encodeURIComponent(namespace)}`

export const upsertBuildSetting = (namespace: string, provider: string) =>
  workflowRequest<BuildSetting>(buildSettingPath(namespace), {
    method: 'PUT',
    body: JSON.stringify({ provider }),
  })

export const deleteBuildSetting = (namespace: string) =>
  workflowRequest<{ message: string }>(buildSettingPath(namespace), {
    method: 'DELETE',
  })

// ── Managed build providers (settings-created k8s build providers) ──

export const fetchBuildProviders = () =>
  workflowRequest<BuildProvidersResponse>('/buildfleet/providers')

// ── Build runtime settings (default runtime profile wiring) ──

export const fetchRuntimeSettings = () =>
  workflowRequest<RuntimeSettings>('/buildfleet/runtime-settings')

export const updateRuntimeSettings = (req: RuntimeSettings) =>
  workflowRequest<RuntimeSettings>('/buildfleet/runtime-settings', {
    method: 'PUT',
    body: JSON.stringify(req),
  })

export const createBuildProvider = (req: CreateBuildProviderRequest) =>
  workflowRequest<BuildProvider>('/buildfleet/providers', {
    method: 'POST',
    body: JSON.stringify(req),
  })

export const deleteBuildProvider = (name: string) =>
  workflowRequest<{ message: string }>(
    `/buildfleet/providers/${encodeURIComponent(name)}`,
    { method: 'DELETE' }
  )

export const updateBuildProvider = (
  name: string,
  req: UpdateBuildProviderRequest
) =>
  workflowRequest<BuildProvider>(
    `/buildfleet/providers/${encodeURIComponent(name)}`,
    { method: 'PUT', body: JSON.stringify(req) }
  )

// Disable = unregister from the build fleet but keep the row (recoverable);
// enable re-registers it.
export const setBuildProviderEnabled = (name: string, enabled: boolean) =>
  workflowRequest<{ message: string }>(
    `/buildfleet/providers/${encodeURIComponent(name)}/enabled`,
    { method: 'PUT', body: JSON.stringify({ enabled }) }
  )

// ── Query hooks ──

const workflowKeys = {
  all: ['workflow'] as const,
  list: () => [...workflowKeys.all, 'list'] as const,
  detail: (id: number) => [...workflowKeys.all, 'detail', id] as const,
  runs: (id: number) => [...workflowKeys.all, 'runs', id] as const,
  run: (id: number) => [...workflowKeys.all, 'run', id] as const,
  revisions: (id: number) => [...workflowKeys.all, 'revisions', id] as const,
  credentials: () => [...workflowKeys.all, 'credentials'] as const,
  defaultRegistry: () => [...workflowKeys.all, 'default-registry'] as const,
  directory: () => [...workflowKeys.all, 'user-directory'] as const,
  buildSettings: () => [...workflowKeys.all, 'build-settings'] as const,
  buildProviders: () => [...workflowKeys.all, 'build-providers'] as const,
  runtimeSettings: () => [...workflowKeys.all, 'runtime-settings'] as const,
}

export const useWorkflows = () =>
  useQuery({
    queryKey: workflowKeys.list(),
    queryFn: fetchWorkflows,
    select: (data) => data.workflows,
  })

export const useWorkflow = (id: number | undefined) =>
  useQuery({
    queryKey: workflowKeys.detail(id ?? 0),
    queryFn: () => fetchWorkflow(id as number),
    enabled: id !== undefined,
  })

export const useWorkflowRevisions = (id: number | undefined) =>
  useQuery({
    queryKey: workflowKeys.revisions(id ?? 0),
    queryFn: () => fetchWorkflowRevisions(id as number),
    enabled: id !== undefined,
  })

export const useRuns = (id: number | undefined, limit = 50) =>
  useQuery({
    queryKey: [...workflowKeys.runs(id ?? 0), limit],
    queryFn: () => fetchRuns(id as number, limit),
    enabled: id !== undefined,
  })

// Latest run per workflow for the list page (最近运行 column and the
// run-status filter), batched through useQueries instead of one query per
// row. IDs still loading appear in pendingIds so cells keep their
// placeholder; a workflow without runs maps to undefined.
export const useLatestRuns = (ids: number[]) =>
  useQueries({
    queries: ids.map((id) => ({
      queryKey: [...workflowKeys.runs(id), 'latest'],
      queryFn: () => fetchRuns(id, 1),
    })),
    combine: (results) => {
      const latestById: Record<number, RunSummary | undefined> = {}
      const pendingIds: number[] = []
      ids.forEach((id, i) => {
        if (results[i]?.isPending) {
          pendingIds.push(id)
        } else {
          latestById[id] = results[i]?.data?.runs[0]
        }
      })
      return { latestById, pendingIds }
    },
  })

export const useRun = (runId: number | undefined) =>
  useQuery({
    queryKey: workflowKeys.run(runId ?? 0),
    queryFn: () => fetchRun(runId as number),
    enabled: runId !== undefined,
  })

export const useCredentials = () =>
  useQuery({
    queryKey: workflowKeys.credentials(),
    queryFn: fetchCredentials,
    select: (data) => data.credentials,
  })

// Account list for the approval assignee picker. A 403 is not an error: the
// directory is simply unavailable to this user, so the query resolves to an
// empty list plus a forbidden flag and the picker degrades to raw ID input.
export interface UserDirectory {
  users: UserDirectoryEntry[]
  forbidden: boolean
}

export const useUserDirectory = () =>
  useQuery({
    queryKey: workflowKeys.directory(),
    queryFn: async (): Promise<UserDirectory> => {
      try {
        const data = await fetchUserDirectory()
        return { users: data.users, forbidden: false }
      } catch (error) {
        if (error instanceof WorkflowApiError && error.status === 403) {
          return { users: [], forbidden: true }
        }
        throw error
      }
    },
  })

export const useInvalidateWorkflow = () => {
  const queryClient = useQueryClient()
  return () => queryClient.invalidateQueries({ queryKey: workflowKeys.all })
}

export const useSaveWorkflowMutation = () => {
  const invalidate = useInvalidateWorkflow()
  return useMutation({
    mutationFn: ({
      id,
      req,
    }: {
      id?: number
      req: CreateWorkflowRequest | UpdateWorkflowRequest
    }) =>
      id !== undefined
        ? updateWorkflow(id, req as UpdateWorkflowRequest)
        : createWorkflow(req as CreateWorkflowRequest),
    onSuccess: invalidate,
  })
}

export const useDeleteWorkflowMutation = () => {
  const invalidate = useInvalidateWorkflow()
  return useMutation({
    mutationFn: deleteWorkflow,
    onSuccess: invalidate,
  })
}

export const useTriggerRunMutation = (id: number) => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (params: Record<string, unknown>) => triggerRun(id, params),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: workflowKeys.runs(id) }),
  })
}

export const useRunActionMutation = () => {
  const queryClient = useQueryClient()
  const invalidateRun = (runId?: number) => {
    if (runId !== undefined) {
      queryClient.invalidateQueries({ queryKey: workflowKeys.run(runId) })
    }
  }
  const cancel = useMutation({
    mutationFn: (runId: number) => cancelRun(runId),
    onSuccess: (_data, runId) => invalidateRun(runId),
  })
  const retry = useMutation({
    mutationFn: (runId: number) => retryRun(runId),
    onSuccess: (_data, runId) => invalidateRun(runId),
  })
  return { cancel, retry }
}

export const useDecideApprovalMutation = (runId: number) => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({
      nodeId,
      approve,
      comment,
    }: {
      nodeId: string
      approve: boolean
      comment: string
    }) => decideApproval(runId, nodeId, approve, comment),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: workflowKeys.run(runId) }),
  })
}

export const useCreateCredentialMutation = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: createCredential,
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: workflowKeys.credentials() }),
  })
}

export const useUpdateCredentialMutation = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({
      name,
      req,
    }: {
      name: string
      req: { description: string; payload: CredentialPayload }
    }) => updateCredential(name, req),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: workflowKeys.credentials() }),
  })
}

export const useDeleteCredentialMutation = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: deleteCredential,
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: workflowKeys.credentials() }),
  })
}

export const useDefaultRegistry = () =>
  useQuery({
    queryKey: workflowKeys.defaultRegistry(),
    queryFn: fetchDefaultRegistry,
  })

export const useSetDefaultRegistryMutation = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: setDefaultRegistry,
    onSuccess: () =>
      queryClient.invalidateQueries({
        queryKey: workflowKeys.defaultRegistry(),
      }),
  })
}

export const useBuildSettings = () =>
  useQuery({
    queryKey: workflowKeys.buildSettings(),
    queryFn: fetchBuildSettings,
  })

export const useUpsertBuildSettingMutation = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({
      namespace,
      provider,
    }: {
      namespace: string
      provider: string
    }) => upsertBuildSetting(namespace, provider),
    onSuccess: () =>
      queryClient.invalidateQueries({
        queryKey: workflowKeys.buildSettings(),
      }),
  })
}

export const useDeleteBuildSettingMutation = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: deleteBuildSetting,
    onSuccess: () =>
      queryClient.invalidateQueries({
        queryKey: workflowKeys.buildSettings(),
      }),
  })
}

export const useBuildProviders = () =>
  useQuery({
    queryKey: workflowKeys.buildProviders(),
    queryFn: fetchBuildProviders,
  })

export const useRuntimeSettings = () =>
  useQuery({
    queryKey: workflowKeys.runtimeSettings(),
    queryFn: fetchRuntimeSettings,
  })

export const useUpdateRuntimeSettingsMutation = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: updateRuntimeSettings,
    onSuccess: () =>
      queryClient.invalidateQueries({
        queryKey: workflowKeys.runtimeSettings(),
      }),
  })
}

// Provider changes reshape the build-settings provider options too, so both
// query families are invalidated.
const invalidateProviderSurfaces = (
  queryClient: ReturnType<typeof useQueryClient>
) => {
  queryClient.invalidateQueries({ queryKey: workflowKeys.buildProviders() })
  queryClient.invalidateQueries({ queryKey: workflowKeys.buildSettings() })
}

export const useCreateBuildProviderMutation = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: createBuildProvider,
    onSuccess: () => invalidateProviderSurfaces(queryClient),
  })
}

export const useUpdateBuildProviderMutation = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({
      name,
      req,
    }: {
      name: string
      req: UpdateBuildProviderRequest
    }) => updateBuildProvider(name, req),
    onSuccess: () => invalidateProviderSurfaces(queryClient),
  })
}

export const useDeleteBuildProviderMutation = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: deleteBuildProvider,
    onSuccess: () => invalidateProviderSurfaces(queryClient),
  })
}

export const useSetBuildProviderEnabledMutation = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ name, enabled }: { name: string; enabled: boolean }) =>
      setBuildProviderEnabled(name, enabled),
    onSuccess: () => invalidateProviderSurfaces(queryClient),
  })
}

// Re-exported for the settings-sections entry (keeps the host import surface
// to the workflow module only).
export type { CredentialPayload, ParamSpec, TriggerConfig }
