import type {
  Application,
  ApplicationConfig,
  ApplicationDeployRequest,
  ApplicationDeployResponse,
  ApplicationListResponse,
  ApplicationOfflineResponse,
  ApplicationPreviewRequest,
  ApplicationPreviewResponse,
  ApplicationResolveRequest,
  ApplicationRevisionDetail,
  ApplicationRevisionListResponse,
  ApplicationRollbackRequest,
  ApplicationRollbackResponse,
  ApplicationRuntimeView,
  ApplicationStatusReport,
  CreateApplicationRequest,
  SpaceQuotaInput,
  SpaceQuotaListResponse,
  SpaceQuotaUpsertResponse,
  UpdateApplicationRequest,
} from '@/portal/application/types'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { apiClient, type ApiRequestOptions } from '@/lib/api-client'

// Typed API error preserving the JSON body, so callers can act on non-2xx
// answers (409 previewToken / expectedRev mismatches, ...).
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

// App identity path segment: /applications/{space}/{name}/...
const appPath = (space: string, name: string) =>
  `/${encodeURIComponent(space)}/${encodeURIComponent(name)}`

export const fetchApplication = (space: string, name: string) =>
  applicationRequest<Application>(appPath(space, name), {
    method: 'GET',
  })

export const useApplication = (
  space: string | undefined,
  name: string | undefined
) => {
  return useQuery({
    queryKey: ['applications', space, name],
    queryFn: () => fetchApplication(space as string, name as string),
    enabled: !!space && !!name,
    placeholderData: (prevData) => prevData,
  })
}

export const createApplication = (req: CreateApplicationRequest) =>
  applicationRequest<Application>('', {
    method: 'POST',
    body: JSON.stringify(req),
  })

export const updateApplication = (
  space: string,
  name: string,
  req: UpdateApplicationRequest
) =>
  applicationRequest<Application>(appPath(space, name), {
    method: 'PUT',
    body: JSON.stringify(req),
  })

export const deleteApplication = (space: string, name: string) =>
  applicationRequest<{ message: string }>(appPath(space, name), {
    method: 'DELETE',
  })

// ── Per-cluster configs ──

export const fetchApplicationConfig = (
  space: string,
  name: string,
  cluster: string
) =>
  applicationRequest<ApplicationConfig>(
    `${appPath(space, name)}/configs/${encodeURIComponent(cluster)}`,
    { method: 'GET' }
  )

export const useApplicationConfig = (
  space: string | undefined,
  name: string | undefined,
  cluster: string | undefined
) => {
  return useQuery({
    queryKey: ['applications', space, name, 'config', cluster],
    queryFn: () =>
      fetchApplicationConfig(
        space as string,
        name as string,
        cluster as string
      ),
    enabled: !!space && !!name && !!cluster,
    placeholderData: (prevData) => prevData,
  })
}

// ── Deploy / offline ──

// {dryRun: true} runs the full pipeline against the API server with
// dry-run: All without touching cluster state or the database. When the
// target config does not exist yet, {copyFrom} deep-copies a source cluster's
// manifests as the base; without it the base is rendered from App.Spec.
export const deployApplicationConfig = (
  space: string,
  name: string,
  cluster: string,
  req: ApplicationDeployRequest = {}
) =>
  applicationRequest<ApplicationDeployResponse>(
    `${appPath(space, name)}/configs/${encodeURIComponent(cluster)}/deploy`,
    { method: 'POST', body: JSON.stringify(req) }
  )

// Offline always deletes the cluster objects AND the config row with its
// revision chain (irreversible); a partial cluster-side failure keeps the
// config for retry (HTTP 409).
export const offlineApplicationConfig = (
  space: string,
  name: string,
  cluster: string
) =>
  applicationRequest<ApplicationOfflineResponse>(
    `${appPath(space, name)}/configs/${encodeURIComponent(cluster)}/offline`,
    { method: 'POST' }
  )

// ── Resolve (drift side-taking) ──

// Adopts the selected live subtrees into the stored manifests (path-level,
// two-sided documents only) and re-anchors; answers the config's fresh status.
export const resolveApplicationConfig = (
  space: string,
  name: string,
  cluster: string,
  req: ApplicationResolveRequest
) =>
  applicationRequest<ApplicationStatusReport>(
    `${appPath(space, name)}/configs/${encodeURIComponent(cluster)}/resolve`,
    { method: 'POST', body: JSON.stringify(req) }
  )

// ── Status (drift report) ──

export const fetchApplicationStatus = (
  space: string,
  name: string,
  cluster: string
) =>
  applicationRequest<ApplicationStatusReport>(
    `${appPath(space, name)}/configs/${encodeURIComponent(cluster)}/status`,
    { method: 'GET' }
  )

export const useApplicationStatus = (
  space: string | undefined,
  name: string | undefined,
  cluster: string | undefined,
  options?: { disable?: boolean; refreshInterval?: number }
) => {
  return useQuery({
    queryKey: ['applications', space, name, 'status', cluster],
    queryFn: () =>
      fetchApplicationStatus(
        space as string,
        name as string,
        cluster as string
      ),
    enabled: !!space && !!name && !!cluster && !options?.disable,
    refetchInterval: options?.refreshInterval || 0,
    placeholderData: (prevData) => prevData,
  })
}

// ── Runtime view / restart ──

export const fetchApplicationResources = (
  space: string,
  name: string,
  cluster: string
) =>
  applicationRequest<ApplicationRuntimeView>(
    `${appPath(space, name)}/configs/${encodeURIComponent(cluster)}/resources`,
    { method: 'GET' }
  )

export const useApplicationResources = (
  space: string | undefined,
  name: string | undefined,
  cluster: string | undefined,
  options?: { disable?: boolean; refreshInterval?: number }
) => {
  return useQuery({
    queryKey: ['applications', space, name, 'resources', cluster],
    queryFn: () =>
      fetchApplicationResources(
        space as string,
        name as string,
        cluster as string
      ),
    enabled: !!space && !!name && !!cluster && !options?.disable,
    refetchInterval: options?.refreshInterval || 0,
    placeholderData: (prevData) => prevData,
  })
}

export const restartApplicationConfig = (
  space: string,
  name: string,
  cluster: string
) =>
  applicationRequest<{ success: boolean; kind: string; name: string }>(
    `${appPath(space, name)}/configs/${encodeURIComponent(cluster)}/restart`,
    { method: 'POST' }
  )

export const restartApplicationPod = (
  space: string,
  name: string,
  cluster: string,
  pod: string
) =>
  applicationRequest<{ success: boolean; pod: string }>(
    `${appPath(space, name)}/configs/${encodeURIComponent(cluster)}/pods/${encodeURIComponent(pod)}/restart`,
    { method: 'POST' }
  )

export const forceDeleteApplicationPod = (
  space: string,
  name: string,
  cluster: string,
  pod: string
) =>
  applicationRequest<{ success: boolean; pod: string }>(
    `${appPath(space, name)}/configs/${encodeURIComponent(cluster)}/pods/${encodeURIComponent(pod)}/force-delete`,
    { method: 'POST' }
  )

// ── Revisions ──

export const fetchApplicationRevisions = (
  space: string,
  name: string,
  cluster: string
) =>
  applicationRequest<ApplicationRevisionListResponse>(
    `${appPath(space, name)}/configs/${encodeURIComponent(cluster)}/revisions`,
    { method: 'GET' }
  )

export const useApplicationRevisions = (
  space: string | undefined,
  name: string | undefined,
  cluster: string | undefined,
  options?: { disable?: boolean }
) => {
  return useQuery({
    queryKey: ['applications', space, name, 'revisions', cluster],
    queryFn: () =>
      fetchApplicationRevisions(
        space as string,
        name as string,
        cluster as string
      ),
    enabled: !!space && !!name && !!cluster && !options?.disable,
    placeholderData: (prevData) => prevData,
  })
}

export const fetchApplicationRevision = (
  space: string,
  name: string,
  cluster: string,
  rev: number
) =>
  applicationRequest<ApplicationRevisionDetail>(
    `${appPath(space, name)}/configs/${encodeURIComponent(cluster)}/revisions/${rev}`,
    { method: 'GET' }
  )

// {rev, deploy:true} rolls back and publishes in one request; without deploy
// the revision manifests only become the pending config.
export const rollbackApplicationConfig = (
  space: string,
  name: string,
  cluster: string,
  req: ApplicationRollbackRequest
) =>
  applicationRequest<ApplicationRollbackResponse>(
    `${appPath(space, name)}/configs/${encodeURIComponent(cluster)}/rollback`,
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
