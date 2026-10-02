// Standalone fetch client for the workflow module (design §11: the module
// ships its own API surface, it does not touch the shared lib/api-client).
// Every call sends cookies and the current-cluster header, mirroring what the
// host client does for module routes mounted under /api/v1.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import {
  appendCurrentClusterHeader,
  appendCurrentClusterParam,
} from '@/lib/current-cluster'
import { withSubPath } from '@/lib/subpath'

import type {
  CreateCredentialRequest,
  CreateWorkflowRequest,
  CredentialPayload,
  CredentialSummary,
  ParamSpec,
  RevisionSummary,
  RunDetail,
  RunSummary,
  TriggerConfig,
  TriggerRunResponse,
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

export const fetchWorkflow = (name: string) =>
  workflowRequest<Workflow>(`/workflows/${encodeURIComponent(name)}`)

export const createWorkflow = (req: CreateWorkflowRequest) =>
  workflowRequest<Workflow>('/workflows', {
    method: 'POST',
    body: JSON.stringify(req),
  })

export const updateWorkflow = (name: string, req: UpdateWorkflowRequest) =>
  workflowRequest<Workflow>(`/workflows/${encodeURIComponent(name)}`, {
    method: 'PUT',
    body: JSON.stringify(req),
  })

export const deleteWorkflow = (name: string) =>
  workflowRequest<{ message: string }>(
    `/workflows/${encodeURIComponent(name)}`,
    {
      method: 'DELETE',
    }
  )

export const fetchWorkflowRevisions = (name: string) =>
  workflowRequest<{ revisions: RevisionSummary[] }>(
    `/workflows/${encodeURIComponent(name)}/revisions`
  )

export const fetchWebhookToken = (name: string) =>
  workflowRequest<{ token: string }>(
    `/workflows/${encodeURIComponent(name)}/webhook-token`
  )

export const resetWebhookToken = (name: string) =>
  workflowRequest<{ token: string }>(
    `/workflows/${encodeURIComponent(name)}/webhook-token/reset`,
    { method: 'POST' }
  )

// ── Runs ──

export const triggerRun = (name: string, params: Record<string, unknown>) =>
  workflowRequest<TriggerRunResponse>(
    `/workflows/${encodeURIComponent(name)}/runs`,
    { method: 'POST', body: JSON.stringify({ params }) }
  )

export const fetchRuns = (name: string, limit = 50) =>
  workflowRequest<{ runs: RunSummary[]; total: number }>(
    `/workflows/${encodeURIComponent(name)}/runs?limit=${limit}`
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

// ── Query hooks ──

const workflowKeys = {
  all: ['workflow'] as const,
  list: () => [...workflowKeys.all, 'list'] as const,
  detail: (name: string) => [...workflowKeys.all, 'detail', name] as const,
  runs: (name: string) => [...workflowKeys.all, 'runs', name] as const,
  run: (id: number) => [...workflowKeys.all, 'run', id] as const,
  revisions: (name: string) =>
    [...workflowKeys.all, 'revisions', name] as const,
  credentials: () => [...workflowKeys.all, 'credentials'] as const,
}

export const useWorkflows = () =>
  useQuery({
    queryKey: workflowKeys.list(),
    queryFn: fetchWorkflows,
    select: (data) => data.workflows,
  })

export const useWorkflow = (name: string | undefined) =>
  useQuery({
    queryKey: workflowKeys.detail(name ?? ''),
    queryFn: () => fetchWorkflow(name as string),
    enabled: !!name,
  })

export const useWorkflowRevisions = (name: string | undefined) =>
  useQuery({
    queryKey: workflowKeys.revisions(name ?? ''),
    queryFn: () => fetchWorkflowRevisions(name as string),
    enabled: !!name,
  })

export const useRuns = (name: string | undefined, limit = 50) =>
  useQuery({
    queryKey: [...workflowKeys.runs(name ?? ''), limit],
    queryFn: () => fetchRuns(name as string, limit),
    enabled: !!name,
  })

// Latest run of a workflow, used by the list page's 最近运行 column.
export const useLatestRun = (name: string, enabled = true) =>
  useQuery({
    queryKey: [...workflowKeys.runs(name), 'latest'],
    queryFn: () => fetchRuns(name, 1),
    enabled,
    select: (data) => data.runs[0],
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

export const useInvalidateWorkflow = () => {
  const queryClient = useQueryClient()
  return () => queryClient.invalidateQueries({ queryKey: workflowKeys.all })
}

export const useSaveWorkflowMutation = () => {
  const invalidate = useInvalidateWorkflow()
  return useMutation({
    mutationFn: ({
      name,
      req,
    }: {
      name?: string
      req: CreateWorkflowRequest | UpdateWorkflowRequest
    }) =>
      name
        ? updateWorkflow(name, req as UpdateWorkflowRequest)
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

export const useTriggerRunMutation = (name: string) => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (params: Record<string, unknown>) => triggerRun(name, params),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: workflowKeys.runs(name) }),
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

// Re-exported for the settings-sections entry (keeps the host import surface
// to the workflow module only).
export type { CredentialPayload, ParamSpec, TriggerConfig }
