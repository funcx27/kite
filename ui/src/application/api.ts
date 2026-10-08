import type {
  Application,
  ApplicationConfig,
  ApplicationDeployRequest,
  ApplicationDeployResponse,
  ApplicationListResponse,
  ApplicationMergePreview,
  ApplicationMergeRequest,
  ApplicationMergeResponse,
  ApplicationOfflineResponse,
  ApplicationPreviewRequest,
  ApplicationPreviewResponse,
  ApplicationRevisionDetail,
  ApplicationRevisionListResponse,
  ApplicationRollbackRequest,
  ApplicationRollbackResponse,
  ApplicationRuntimeView,
  ApplicationStatusReport,
  ApplicationSyncResponse,
  CreateApplicationRequest,
  SpaceQuotaInput,
  SpaceQuotaListResponse,
  SpaceQuotaUpsertResponse,
  UpdateApplicationConfigRequest,
  UpdateApplicationRequest,
} from '@/application/types'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { apiClient, type ApiRequestOptions } from '@/lib/api-client'

// Typed API error preserving the JSON body, so callers can read structured
// payloads of non-2xx answers (deploy 409 conflicts, 409 expectedRev, ...).
export class ApplicationApiError extends Error {
  status: number
  payload: Record<string, unknown>

  constructor(
    status: number,
    message: string,
    payload: Record<string, unknown>
  ) {
    super(message)
    this.name = 'ApplicationApiError'
    this.status = status
    this.payload = payload
  }
}

async function applicationRequest<T>(
  path: string,
  options?: ApiRequestOptions
): Promise<T> {
  const response = await apiClient.request(`/applications${path}`, options)
  const body: unknown = await response.json().catch(() => ({}))
  if (!response.ok) {
    const payload = (body ?? {}) as Record<string, unknown>
    const message =
      typeof payload.error === 'string'
        ? payload.error
        : `HTTP ${response.status}`
    throw new ApplicationApiError(response.status, message, payload)
  }
  return body as T
}

// ── Applications ──

export const fetchApplications = () =>
  applicationRequest<ApplicationListResponse>('', { method: 'GET' })

export const useApplications = (options?: { disable?: boolean }) => {
  return useQuery({
    queryKey: ['applications'],
    queryFn: fetchApplications,
    enabled: !options?.disable,
    select: (data: ApplicationListResponse) => data.applications,
    placeholderData: (prevData) => prevData,
  })
}

export const fetchApplication = (name: string) =>
  applicationRequest<Application>(`/${encodeURIComponent(name)}`, {
    method: 'GET',
  })

export const useApplication = (name: string | undefined) => {
  return useQuery({
    queryKey: ['applications', name],
    queryFn: () => fetchApplication(name as string),
    enabled: !!name,
    placeholderData: (prevData) => prevData,
  })
}

export const createApplication = (req: CreateApplicationRequest) =>
  applicationRequest<Application>('', {
    method: 'POST',
    body: JSON.stringify(req),
  })

export const updateApplication = (
  name: string,
  req: UpdateApplicationRequest
) =>
  applicationRequest<Application>(`/${encodeURIComponent(name)}`, {
    method: 'PUT',
    body: JSON.stringify(req),
  })

export const deleteApplication = (name: string) =>
  applicationRequest<{ message: string }>(`/${encodeURIComponent(name)}`, {
    method: 'DELETE',
  })

// ── Per-cluster configs ──

export const fetchApplicationConfig = (name: string, cluster: string) =>
  applicationRequest<ApplicationConfig>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}`,
    { method: 'GET' }
  )

export const useApplicationConfig = (
  name: string | undefined,
  cluster: string | undefined
) => {
  return useQuery({
    queryKey: ['applications', name, 'config', cluster],
    queryFn: () => fetchApplicationConfig(name as string, cluster as string),
    enabled: !!name && !!cluster,
    placeholderData: (prevData) => prevData,
  })
}

// Optimistic-locked save: expectedRev must match the config's latest
// revision; carries either {spec} (injected onto the manifests) or
// {manifests} (full replacement).
export const updateApplicationConfig = (
  name: string,
  cluster: string,
  req: UpdateApplicationConfigRequest
) =>
  applicationRequest<ApplicationConfig>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}`,
    { method: 'PUT', body: JSON.stringify(req) }
  )

// ── Deploy / sync / offline ──

// {dryRun: true} runs the full pipeline against the API server with
// dry-run: All without touching cluster state or the database. When the
// target config does not exist yet, {copyFrom} deep-copies a source cluster's
// manifests as the base; without it the base is rendered from App.Spec.
export const deployApplicationConfig = (
  name: string,
  cluster: string,
  req: ApplicationDeployRequest = {}
) =>
  applicationRequest<ApplicationDeployResponse>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}/deploy`,
    { method: 'POST', body: JSON.stringify(req) }
  )

export const syncApplicationConfig = (name: string, cluster: string) =>
  applicationRequest<ApplicationSyncResponse>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}/sync`,
    { method: 'POST' }
  )

// Offline always deletes the cluster objects AND the config row with its
// revision chain (irreversible); a partial cluster-side failure keeps the
// config for retry (HTTP 409).
export const offlineApplicationConfig = (name: string, cluster: string) =>
  applicationRequest<ApplicationOfflineResponse>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}/offline`,
    { method: 'POST' }
  )

// ── Conflict merge ──

export const fetchApplicationMergePreview = (name: string, cluster: string) =>
  applicationRequest<ApplicationMergePreview>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}/merge-preview`,
    { method: 'GET' }
  )

export const useApplicationMergePreview = (
  name: string | undefined,
  cluster: string | undefined,
  options?: { disable?: boolean }
) => {
  return useQuery({
    queryKey: ['applications', name, 'merge-preview', cluster],
    queryFn: () =>
      fetchApplicationMergePreview(name as string, cluster as string),
    enabled: !!name && !!cluster && !options?.disable,
    placeholderData: (prevData) => prevData,
  })
}

// choices keys are scoped paths "<docKey>:<path>" (MERGE_DOC_ROOT_PATH for
// document-level collisions); rollbacks lists auto-adopted paths reverted to
// the baseline, persisted as OverriddenPaths.
export const mergeApplicationConfig = (
  name: string,
  cluster: string,
  req: ApplicationMergeRequest = {}
) =>
  applicationRequest<ApplicationMergeResponse>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}/merge`,
    { method: 'POST', body: JSON.stringify(req) }
  )

// ── Status (drift report) ──

export const fetchApplicationStatus = (name: string, cluster: string) =>
  applicationRequest<ApplicationStatusReport>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}/status`,
    { method: 'GET' }
  )

export const useApplicationStatus = (
  name: string | undefined,
  cluster: string | undefined,
  options?: { disable?: boolean; refreshInterval?: number }
) => {
  return useQuery({
    queryKey: ['applications', name, 'status', cluster],
    queryFn: () => fetchApplicationStatus(name as string, cluster as string),
    enabled: !!name && !!cluster && !options?.disable,
    refetchInterval: options?.refreshInterval || 0,
    placeholderData: (prevData) => prevData,
  })
}

// ── Runtime view / restart ──

export const fetchApplicationResources = (name: string, cluster: string) =>
  applicationRequest<ApplicationRuntimeView>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}/resources`,
    { method: 'GET' }
  )

export const useApplicationResources = (
  name: string | undefined,
  cluster: string | undefined,
  options?: { disable?: boolean; refreshInterval?: number }
) => {
  return useQuery({
    queryKey: ['applications', name, 'resources', cluster],
    queryFn: () => fetchApplicationResources(name as string, cluster as string),
    enabled: !!name && !!cluster && !options?.disable,
    refetchInterval: options?.refreshInterval || 0,
    placeholderData: (prevData) => prevData,
  })
}

export const restartApplicationConfig = (name: string, cluster: string) =>
  applicationRequest<{ success: boolean; kind: string; name: string }>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}/restart`,
    { method: 'POST' }
  )

export const restartApplicationPod = (
  name: string,
  cluster: string,
  pod: string
) =>
  applicationRequest<{ success: boolean; pod: string }>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}/pods/${encodeURIComponent(pod)}/restart`,
    { method: 'POST' }
  )

export const forceDeleteApplicationPod = (
  name: string,
  cluster: string,
  pod: string
) =>
  applicationRequest<{ success: boolean; pod: string }>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}/pods/${encodeURIComponent(pod)}/force-delete`,
    { method: 'POST' }
  )

// ── Revisions ──

export const fetchApplicationRevisions = (name: string, cluster: string) =>
  applicationRequest<ApplicationRevisionListResponse>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}/revisions`,
    { method: 'GET' }
  )

export const useApplicationRevisions = (
  name: string | undefined,
  cluster: string | undefined,
  options?: { disable?: boolean }
) => {
  return useQuery({
    queryKey: ['applications', name, 'revisions', cluster],
    queryFn: () => fetchApplicationRevisions(name as string, cluster as string),
    enabled: !!name && !!cluster && !options?.disable,
    placeholderData: (prevData) => prevData,
  })
}

export const fetchApplicationRevision = (
  name: string,
  cluster: string,
  rev: number
) =>
  applicationRequest<ApplicationRevisionDetail>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}/revisions/${rev}`,
    { method: 'GET' }
  )

// {rev, deploy:true} rolls back and publishes in one request; without deploy
// the revision manifests only become the pending config.
export const rollbackApplicationConfig = (
  name: string,
  cluster: string,
  req: ApplicationRollbackRequest
) =>
  applicationRequest<ApplicationRollbackResponse>(
    `/${encodeURIComponent(name)}/configs/${encodeURIComponent(cluster)}/rollback`,
    { method: 'POST', body: JSON.stringify(req) }
  )

// ── Preview ──

export const previewApplicationRender = (req: ApplicationPreviewRequest) =>
  applicationRequest<ApplicationPreviewResponse>('/preview', {
    method: 'POST',
    body: JSON.stringify(req),
  })

// ── Space quotas ──

export const fetchSpaceQuotas = (name: string) =>
  applicationRequest<SpaceQuotaListResponse>(
    `/spaces/${encodeURIComponent(name)}/quotas`,
    { method: 'GET' }
  )

export const useSpaceQuotas = (name: string | undefined) => {
  return useQuery({
    queryKey: ['applications', 'space-quotas', name],
    queryFn: () => fetchSpaceQuotas(name as string),
    enabled: !!name,
    placeholderData: (prevData) => prevData,
  })
}

// Idempotent per-cluster upsert: creates the labeled space namespace where
// missing and (re)applies the kite-managed ResourceQuota. Partial failures
// come back in the response body, not as a thrown error.
export const upsertSpaceQuotas = (name: string, quotas: SpaceQuotaInput[]) =>
  applicationRequest<SpaceQuotaUpsertResponse>(
    `/spaces/${encodeURIComponent(name)}/quotas`,
    { method: 'PUT', body: JSON.stringify({ quotas }) }
  )

export const useUpsertSpaceQuotasMutation = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({
      name,
      quotas,
    }: {
      name: string
      quotas: SpaceQuotaInput[]
    }) => upsertSpaceQuotas(name, quotas),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: ['applications', 'space-quotas'],
      })
      // A newly created space namespace only shows up in the picker after
      // the namespace list cache is dropped.
      void queryClient.invalidateQueries({ queryKey: ['namespaces'] })
    },
  })
}

// Unlinking strips the space marker from the namespace on the given clusters;
// the namespace and its objects stay.
export const dissociateSpace = (name: string, clusters: string[]) =>
  applicationRequest<SpaceQuotaUpsertResponse>(
    `/spaces/${encodeURIComponent(name)}`,
    { method: 'DELETE', body: JSON.stringify({ clusters }) }
  )

export const useDissociateSpaceMutation = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ name, clusters }: { name: string; clusters: string[] }) =>
      dissociateSpace(name, clusters),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: ['applications', 'space-quotas'],
      })
      void queryClient.invalidateQueries({ queryKey: ['namespaces'] })
    },
  })
}
