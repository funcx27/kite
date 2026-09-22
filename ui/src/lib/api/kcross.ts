import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { apiClient } from '../api-client'

export interface KcrossSetting {
  enabled: boolean
  gateways: string[]
  adminTokenConfigured: boolean
}

export interface KcrossSettingUpdate {
  enabled?: boolean
  gateways?: string
  adminToken?: string
}

export interface KcrossCapability {
  name: string
  kind: string
  health: string
  endpoint?: string
  /** 经网关的接入地址：http=反代子路径，tcp=网关端口映射 */
  access?: string
}

export interface KcrossPeer {
  id: string
  status: 'online' | 'offline'
  connectedAt?: string
  capabilities: KcrossCapability[]
  gateway: string
  kubernetes?: KcrossCapability
  prometheus?: KcrossCapability
  bound: boolean
  clusterId?: number
  clusterName?: string
}

export interface KcrossBinding {
  id: number
  clusterId: number
  peerId: string
  capability: string
}

async function requestJson<T>(url: string, options: RequestInit = {}): Promise<T> {
  const response = await apiClient.request(url, options)
  const data = await response.json().catch(() => ({}))
  if (!response.ok) {
    throw new Error((data as { error?: string }).error || `request failed: ${response.status}`)
  }
  return data as T
}

export const fetchKcrossSetting = (): Promise<KcrossSetting> =>
  requestJson('/admin/kcross/setting')

export const updateKcrossSetting = (
  payload: KcrossSettingUpdate
): Promise<KcrossSetting> =>
  requestJson('/admin/kcross/setting', {
    method: 'PUT',
    body: JSON.stringify(payload),
  })

export const testKcrossSetting = (
  payload: KcrossSettingUpdate
): Promise<{ message: string; peerCount: number }> =>
  requestJson('/admin/kcross/setting/test', {
    method: 'POST',
    body: JSON.stringify(payload),
  })

export const fetchKcrossPeers = (): Promise<KcrossPeer[]> =>
  requestJson<{ peers: KcrossPeer[] }>('/admin/kcross/discover').then(
    (data) => data.peers || []
  )

export const fetchKcrossBindings = (): Promise<KcrossBinding[]> =>
  requestJson<{ bindings: KcrossBinding[] }>('/admin/kcross/bindings').then(
    (data) => data.bindings || []
  )

export const createKcrossBinding = (payload: {
  clusterId: number
  peerId: string
  gateway: string
  capability?: string
}): Promise<KcrossBinding> =>
  requestJson('/admin/kcross/bindings', {
    method: 'POST',
    body: JSON.stringify(payload),
  })

export const useKcrossSetting = (enabled = true) =>
  useQuery({
    queryKey: ['kcross-setting'],
    queryFn: fetchKcrossSetting,
    enabled,
    retry: false,
  })

export const useKcrossPeers = (enabled: boolean) =>
  useQuery({
    queryKey: ['kcross-peers'],
    queryFn: fetchKcrossPeers,
    enabled,
    retry: false,
    refetchInterval: 5 * 1000,
  })

export const useKcrossBindings = () =>
  useQuery({
    queryKey: ['kcross-bindings'],
    queryFn: fetchKcrossBindings,
    retry: false,
  })

export const useInvalidateKcross = () => {
  const queryClient = useQueryClient()
  return () => {
    queryClient.invalidateQueries({ queryKey: ['kcross-peers'] })
    queryClient.invalidateQueries({ queryKey: ['kcross-bindings'] })
    queryClient.invalidateQueries({ queryKey: ['clusters'] })
  }
}

export const useCreateKcrossBinding = () => {
  const invalidate = useInvalidateKcross()
  return useMutation({
    mutationFn: createKcrossBinding,
    onSuccess: invalidate,
  })
}
