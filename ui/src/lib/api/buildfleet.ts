import { useQuery } from '@tanstack/react-query'

import {
  BuildErrorEvent,
  BuildLogEvent,
  BuildRequest,
  BuildResultEvent,
  BuildRuntimeEvent,
  BuildStatus,
  BuildVertexEvent,
  CacheStat,
} from '@/types/buildfleet'
import { readAIChatSSEStream } from '@/components/ai-chat/ai-chat-stream'

import { apiClient } from '../api-client'
import { appendCurrentClusterHeader } from '../current-cluster'
import { withSubPath } from '../subpath'

export interface BuildStreamHandlers {
  onRuntime?: (event: BuildRuntimeEvent) => void
  onVertex?: (event: BuildVertexEvent) => void
  onLog?: (event: BuildLogEvent) => void
  onResult?: (event: BuildResultEvent) => void
  onError?: (event: BuildErrorEvent) => void
}

// startBuild POSTs the build request and parses the SSE progress stream.
// Terminal events (result/error) end the stream; ": ping" keep-alive
// comments are ignored by the parser.
export async function startBuild(
  request: BuildRequest,
  handlers: BuildStreamHandlers,
  signal?: AbortSignal
): Promise<void> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  }
  appendCurrentClusterHeader(headers)

  const response = await fetch(withSubPath('/api/v1/buildfleet/builds'), {
    method: 'POST',
    credentials: 'include',
    headers,
    body: JSON.stringify(request),
    signal,
  })

  if (!response.ok) {
    const errData = await response.json().catch(() => ({}))
    throw new Error(errData.error || `HTTP error! status: ${response.status}`)
  }

  await readAIChatSSEStream(response, (eventType, data) => {
    switch (eventType) {
      case 'runtime':
        handlers.onRuntime?.(data as unknown as BuildRuntimeEvent)
        break
      case 'vertex':
        handlers.onVertex?.(data as unknown as BuildVertexEvent)
        break
      case 'log':
        handlers.onLog?.(data as unknown as BuildLogEvent)
        break
      case 'result':
        handlers.onResult?.(data as unknown as BuildResultEvent)
        break
      case 'error':
        handlers.onError?.(data as unknown as BuildErrorEvent)
        break
    }
  })
}

export const fetchBuildStatus = (): Promise<BuildStatus> => {
  return apiClient.get<BuildStatus>('/buildfleet/status')
}

export const useBuildStatus = (options?: {
  enabled?: boolean
  refetchInterval?: number
}) => {
  return useQuery({
    queryKey: ['buildfleet-status'],
    queryFn: fetchBuildStatus,
    enabled: options?.enabled,
    refetchInterval: options?.refetchInterval ?? 15000,
  })
}

export const fetchCacheStat = (): Promise<CacheStat> => {
  return apiClient.get<CacheStat>('/buildfleet/cachestat')
}

// 501 body when no provider enables a cache registry (adapter
// handleCacheStat); surfaced as an error by apiClient.
export const noCacheRegistryError = 'no cache registry configured'

export const useCacheStat = (options?: { enabled?: boolean }) => {
  return useQuery({
    queryKey: ['buildfleet-cachestat'],
    queryFn: fetchCacheStat,
    enabled: options?.enabled,
    retry: false,
    staleTime: 30000,
  })
}
