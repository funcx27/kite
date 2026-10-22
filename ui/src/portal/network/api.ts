// Network module API client. Endpoints mirror the frozen contract
// (TECH-DESIGN §3): typed JSON bodies, errors as {"error": string} with
// 400/403/404/409 semantics; HostOverview lives in ./types.
import type {
  NetworkAnnotationKey,
  NetworkHostOverview,
  NetworkIngressClass,
  NetworkService,
} from '@/portal/network/types'
import type { APIRequestOptions } from '@kite-dev/plugin-sdk/api'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { apiClient } from '@/lib/api-client'

// Typed API error preserving the JSON body, so callers can act on non-2xx
// answers (409 ownership conflicts, 409 referenced-key deletes, ...).
export class NetworkApiError extends Error {
  status: number
  payload: Record<string, unknown>

  constructor(
    status: number,
    message: string,
    payload: Record<string, unknown>
  ) {
    super(message)
    this.name = 'NetworkApiError'
    this.status = status
    this.payload = payload
  }
}

async function networkRequest<T>(
  path: string,
  options?: APIRequestOptions
): Promise<T> {
  const response = await apiClient.request(`/network${path}`, options)
  const body: unknown = await response.json().catch(() => ({}))
  if (!response.ok) {
    const payload = (body ?? {}) as Record<string, unknown>
    const message =
      typeof payload.error === 'string'
        ? payload.error
        : `HTTP ${response.status}`
    throw new NetworkApiError(response.status, message, payload)
  }
  return body as T
}

// Same contract as networkRequest for the admin group (/admin/network):
// annotation key catalog writes behind RequireAdmin.
async function networkAdminRequest<T>(
  path: string,
  options?: APIRequestOptions
): Promise<T> {
  const response = await apiClient.request(`/admin/network${path}`, options)
  const body: unknown = await response.json().catch(() => ({}))
  if (!response.ok) {
    const payload = (body ?? {}) as Record<string, unknown>
    const message =
      typeof payload.error === 'string'
        ? payload.error
        : `HTTP ${response.status}`
    throw new NetworkApiError(response.status, message, payload)
  }
  return body as T
}

// ── Request/response envelopes ──

export interface NetworkHostListResponse {
  hosts: NetworkHostOverview[]
}

// tlsCertificate omitted/empty = no certificate.
export interface CreateNetworkHostRequest {
  host: string
  cluster: string
  ingressClass: string
  tlsCertificate?: string
}

// Omitted fields keep their stored value; tlsCertificate explicitly null
// removes the certificate.
export interface UpdateNetworkHostSettingsRequest {
  ingressClass?: string
  tlsCertificate?: string | null
}

// Route write shape; appNamespace is derived from the host's space
// server-side, so it is not part of requests. annotations carries the
// target service's service-level annotations (normalized server-side).
export interface NetworkRouteInput {
  path: string
  appName: string
  port: number
  annotations?: Record<string, string>
}

export interface NetworkIngressClassListResponse {
  ingressClasses: NetworkIngressClass[]
}

export interface NetworkServiceListResponse {
  services: NetworkService[]
}

export interface NetworkAnnotationKeyListResponse {
  annotationKeys: NetworkAnnotationKey[]
}

export interface CreateNetworkAnnotationKeyRequest {
  key: string
  description: string
  ingressClass: string
  defaultValue: string
}

// The key itself is immutable; only these fields update.
export interface UpdateNetworkAnnotationKeyRequest {
  description: string
  ingressClass: string
  defaultValue: string
}

// One "key,description" line per entry; the server parses and counts skips.
export interface ImportNetworkAnnotationKeysRequest {
  ingressClass: string
  text: string
}

export interface NetworkAnnotationKeysImportResponse {
  imported: number
  skipped: number
}

// ── Queries ──

export const fetchNetworkHosts = (space: string, cluster?: string) =>
  networkRequest<NetworkHostListResponse>(
    `/${encodeURIComponent(space)}/hosts${
      cluster ? `?cluster=${encodeURIComponent(cluster)}` : ''
    }`,
    { method: 'GET' }
  )

export const useNetworkHosts = (
  space: string | undefined,
  cluster?: string
) => {
  return useQuery({
    queryKey: ['network', 'hosts', space, cluster],
    queryFn: () => fetchNetworkHosts(space as string, cluster),
    enabled: !!space,
    select: (data) => data.hosts,
    placeholderData: (prevData) => prevData,
  })
}

export const fetchAnnotationKeys = () =>
  networkRequest<NetworkAnnotationKeyListResponse>('/annotation-keys', {
    method: 'GET',
  })

export const useAnnotationKeys = () => {
  return useQuery({
    queryKey: ['network', 'annotation-keys'],
    queryFn: fetchAnnotationKeys,
    select: (data) => data.annotationKeys,
    placeholderData: (prevData) => prevData,
  })
}

export const fetchNetworkIngressClasses = (cluster: string) =>
  networkRequest<NetworkIngressClassListResponse>(
    `/ingressclasses/${encodeURIComponent(cluster)}`,
    { method: 'GET' }
  )

export const useNetworkIngressClasses = (cluster: string | undefined) => {
  return useQuery({
    queryKey: ['network', 'ingressclasses', cluster],
    queryFn: () => fetchNetworkIngressClasses(cluster as string),
    enabled: !!cluster,
    select: (data) => data.ingressClasses,
    placeholderData: (prevData) => prevData,
  })
}

export const useNetworkServices = (
  cluster: string | undefined,
  namespace: string | undefined
) => {
  return useQuery({
    queryKey: ['network', 'services', cluster, namespace],
    queryFn: () =>
      networkRequest<NetworkServiceListResponse>(
        `/services/${encodeURIComponent(cluster as string)}/${encodeURIComponent(
          namespace as string
        )}`,
        { method: 'GET' }
      ),
    enabled: !!cluster && !!namespace,
    select: (data) => data.services,
    placeholderData: (prevData) => prevData,
  })
}

// ── Host mutations ──

const hostPath = (space: string, cluster: string, host: string) =>
  `/${encodeURIComponent(space)}/hosts/${encodeURIComponent(
    cluster
  )}/${encodeURIComponent(host)}`

export const createHost = (space: string, req: CreateNetworkHostRequest) =>
  networkRequest<NetworkHostOverview>(`/${encodeURIComponent(space)}/hosts`, {
    method: 'POST',
    body: JSON.stringify(req),
  })

export const updateHostSettings = (
  space: string,
  cluster: string,
  host: string,
  req: UpdateNetworkHostSettingsRequest
) =>
  networkRequest<NetworkHostOverview>(hostPath(space, cluster, host), {
    method: 'PATCH',
    body: JSON.stringify(req),
  })

// Full route-table replacement (PUT semantics); an empty array clears it.
export const replaceRoutes = (
  space: string,
  cluster: string,
  host: string,
  routes: NetworkRouteInput[]
) =>
  networkRequest<NetworkHostOverview>(
    `${hostPath(space, cluster, host)}/routes`,
    { method: 'PUT', body: JSON.stringify({ routes }) }
  )

// Full annotation replacement; keys must exist in the catalog and match the
// host's ingress class.
export const replaceAnnotations = (
  space: string,
  cluster: string,
  host: string,
  annotations: Record<string, string>
) =>
  networkRequest<NetworkHostOverview>(
    `${hostPath(space, cluster, host)}/annotations`,
    { method: 'PUT', body: JSON.stringify({ annotations }) }
  )

export const publishHost = (space: string, cluster: string, host: string) =>
  networkRequest<NetworkHostOverview>(
    `${hostPath(space, cluster, host)}/publish`,
    { method: 'POST' }
  )

// Re-pushes the TLS secret only; 400 when the host has no certificate.
export const syncCert = (space: string, cluster: string, host: string) =>
  networkRequest<NetworkHostOverview>(
    `${hostPath(space, cluster, host)}/sync-cert`,
    { method: 'POST' }
  )

// Partial cluster-side failures answer 409 and keep the row for retry.
// scope=cluster removes only the cluster Ingress/Secret and resets the row
// to unpublished; scope=all (the default behavior) also deletes the row.
export type OfflineScope = 'cluster' | 'all'

export const offlineHost = (
  space: string,
  cluster: string,
  host: string,
  scope: OfflineScope
) =>
  // scope=all answers {message}; scope=cluster answers the refreshed
  // hostOverview after the row reset.
  networkRequest<NetworkHostOverview | { message: string }>(
    `${hostPath(space, cluster, host)}?scope=${scope}`,
    { method: 'DELETE' }
  )

// Export renders the host's desired Ingress objects as a multi-document
// YAML stream (Content-Type text/yaml); goes through apiClient for the
// shared auth + 401-retry + text body handling.
export const fetchHostExport = async (
  space: string,
  cluster: string,
  host: string
): Promise<string> => {
  const response = await apiClient.request(
    `/network${hostPath(space, cluster, host)}/export`,
    { method: 'GET' }
  )
  if (!response.ok) {
    const body: unknown = await response.json().catch(() => ({}))
    const payload = (body ?? {}) as Record<string, unknown>
    throw new NetworkApiError(
      response.status,
      typeof payload.error === 'string'
        ? payload.error
        : `HTTP ${response.status}`,
      payload
    )
  }
  return response.text()
}

// Host writes flip server-derived row state, so every host mutation drops
// the hosts cache on success.
const useInvalidateNetworkHosts = () => {
  const queryClient = useQueryClient()
  return () => {
    void queryClient.invalidateQueries({ queryKey: ['network', 'hosts'] })
  }
}

interface HostMutationVars {
  space: string
  cluster: string
  host: string
}

export const useCreateHostMutation = () => {
  const invalidate = useInvalidateNetworkHosts()
  return useMutation({
    mutationFn: ({
      space,
      req,
    }: {
      space: string
      req: CreateNetworkHostRequest
    }) => createHost(space, req),
    onSuccess: invalidate,
  })
}

export const useUpdateHostSettingsMutation = () => {
  const invalidate = useInvalidateNetworkHosts()
  return useMutation({
    mutationFn: ({
      space,
      cluster,
      host,
      req,
    }: HostMutationVars & { req: UpdateNetworkHostSettingsRequest }) =>
      updateHostSettings(space, cluster, host, req),
    onSuccess: invalidate,
  })
}

export const useReplaceRoutesMutation = () => {
  const invalidate = useInvalidateNetworkHosts()
  return useMutation({
    mutationFn: ({
      space,
      cluster,
      host,
      routes,
    }: HostMutationVars & { routes: NetworkRouteInput[] }) =>
      replaceRoutes(space, cluster, host, routes),
    onSuccess: invalidate,
  })
}

export const useReplaceAnnotationsMutation = () => {
  const invalidate = useInvalidateNetworkHosts()
  return useMutation({
    mutationFn: ({
      space,
      cluster,
      host,
      annotations,
    }: HostMutationVars & { annotations: Record<string, string> }) =>
      replaceAnnotations(space, cluster, host, annotations),
    onSuccess: invalidate,
  })
}

export const usePublishHostMutation = () => {
  const invalidate = useInvalidateNetworkHosts()
  return useMutation({
    mutationFn: ({ space, cluster, host }: HostMutationVars) =>
      publishHost(space, cluster, host),
    onSuccess: invalidate,
  })
}

export const useSyncCertMutation = () => {
  const invalidate = useInvalidateNetworkHosts()
  return useMutation({
    mutationFn: ({ space, cluster, host }: HostMutationVars) =>
      syncCert(space, cluster, host),
    onSuccess: invalidate,
  })
}

export const useOfflineHostMutation = () => {
  const invalidate = useInvalidateNetworkHosts()
  return useMutation({
    mutationFn: ({
      space,
      cluster,
      host,
      scope,
    }: HostMutationVars & { scope: OfflineScope }) =>
      offlineHost(space, cluster, host, scope),
    onSuccess: invalidate,
  })
}

// ── Annotation key catalog admin writes ──

const annotationKeyPath = (id: number) =>
  `/annotation-keys/${encodeURIComponent(id)}`

export const addAnnotationKey = (req: CreateNetworkAnnotationKeyRequest) =>
  networkAdminRequest<NetworkAnnotationKey>('/annotation-keys', {
    method: 'POST',
    body: JSON.stringify(req),
  })

export const updateAnnotationKey = (
  id: number,
  req: UpdateNetworkAnnotationKeyRequest
) =>
  networkAdminRequest<NetworkAnnotationKey>(annotationKeyPath(id), {
    method: 'PUT',
    body: JSON.stringify(req),
  })

// Answers 409 with {error, references} when any host still uses the key.
export const deleteAnnotationKey = (id: number) =>
  networkAdminRequest<{ message: string }>(annotationKeyPath(id), {
    method: 'DELETE',
  })

export const importAnnotationKeys = (req: ImportNetworkAnnotationKeysRequest) =>
  networkAdminRequest<NetworkAnnotationKeysImportResponse>(
    '/annotation-keys/import',
    { method: 'POST', body: JSON.stringify(req) }
  )

// Catalog writes refresh the shared annotation-keys cache: the host
// annotation dialog and the admin catalog page read the same list.
const useInvalidateAnnotationKeys = () => {
  const queryClient = useQueryClient()
  return () => {
    void queryClient.invalidateQueries({
      queryKey: ['network', 'annotation-keys'],
    })
  }
}

export const useAddAnnotationKeyMutation = () => {
  const invalidate = useInvalidateAnnotationKeys()
  return useMutation({
    mutationFn: addAnnotationKey,
    onSuccess: invalidate,
  })
}

export const useUpdateAnnotationKeyMutation = () => {
  const invalidate = useInvalidateAnnotationKeys()
  return useMutation({
    mutationFn: ({
      id,
      req,
    }: {
      id: number
      req: UpdateNetworkAnnotationKeyRequest
    }) => updateAnnotationKey(id, req),
    onSuccess: invalidate,
  })
}

export const useDeleteAnnotationKeyMutation = () => {
  const invalidate = useInvalidateAnnotationKeys()
  return useMutation({
    mutationFn: deleteAnnotationKey,
    onSuccess: invalidate,
  })
}

export const useImportAnnotationKeysMutation = () => {
  const invalidate = useInvalidateAnnotationKeys()
  return useMutation({
    mutationFn: importAnnotationKeys,
    onSuccess: invalidate,
  })
}
