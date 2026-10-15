// Cross-cluster API surface for the platform/application modules (alignment
// plan item 10): the shared client under lib/api stays byte-identical to
// upstream and carries no per-request cluster addressing, so module pages
// that target an explicit cluster (application pod page, space picker, quota
// editor) route through these wrappers instead.
//
// Addressing uses the /_clusters/<name> path prefix, and the x-cluster-name
// query param for WebSockets: the backend resolves the path param first, then
// the header, then the query param (pkg/middleware/cluster.go), so the
// ambient global cluster that apiClient attaches never overrides the explicit
// target (routes.go registers every protected route under /_clusters/:cluster
// as well).
import { useCallback, useEffect, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'

import { PodMetrics } from '@/types/api'
import { API_BASE_URL, apiClient } from '@/lib/api-client'
import type { FileInfo } from '@/lib/api/core'
import {
  appendCurrentClusterParam,
  withCurrentClusterPath,
} from '@/lib/current-cluster'
import { getWebSocketUrl, withSubPath } from '@/lib/subpath'
import useWebSocket, { WebSocketMessage } from '@/lib/useWebSocket'
import { useCluster } from '@/hooks/use-cluster'

// Header targeting one specific cluster, overriding the ambient global one
// for a single request. Only useful for hand-rolled fetches: requests routed
// through apiClient get their header overwritten by the global cluster, so
// prefer withClusterPath there.
export function clusterHeader(cluster: string): Record<string, string> {
  return { 'x-cluster-name': encodeURIComponent(cluster) }
}

// Cluster-scoped path: /_clusters/<cluster><path>. An absent cluster falls
// back to the global switcher, matching withCurrentClusterPath's semantics.
export function withClusterPath(path: string, cluster?: string): string {
  return withCurrentClusterPath(path, cluster)
}

async function podListFiles(
  namespace: string,
  podName: string,
  container: string,
  path: string,
  cluster?: string
): Promise<FileInfo[]> {
  const params = new URLSearchParams({
    container,
    path,
  })
  return apiClient.get<FileInfo[]>(
    `${withClusterPath(`/pods/${namespace}/${podName}/files`, cluster)}?${params.toString()}`
  )
}

export const podDownloadFile = (
  namespace: string,
  podName: string,
  container: string,
  path: string,
  cluster?: string
) => {
  const params = new URLSearchParams({
    container,
    path,
  })
  const url = withSubPath(
    `${API_BASE_URL}${withClusterPath(`/pods/${namespace}/${podName}/files/download`, cluster)}?${params.toString()}`
  )
  window.open(url, '_blank')
}

export const podPreviewFile = (
  namespace: string,
  podName: string,
  container: string,
  path: string,
  cluster?: string
) => {
  const params = new URLSearchParams({
    container,
    path,
  })
  const url = withSubPath(
    `${API_BASE_URL}${withClusterPath(`/pods/${namespace}/${podName}/files/preview`, cluster)}?${params.toString()}`
  )
  window.open(url, '_blank')
}

export const podUploadFile = async (
  namespace: string,
  podName: string,
  container: string,
  path: string,
  file: File,
  cluster?: string
): Promise<void> => {
  const formData = new FormData()
  formData.append('file', file)
  const params = new URLSearchParams({
    container,
    path,
  })

  await apiClient.put(
    `${withClusterPath(`/pods/${namespace}/${podName}/files/upload`, cluster)}?${params.toString()}`,
    formData
  )
}

export const usePodFiles = (
  namespace: string,
  podName: string,
  container: string,
  path: string,
  options?: { enabled?: boolean; cluster?: string }
) => {
  const cluster = options?.cluster
  return useQuery({
    queryKey: ['pod-files', cluster, namespace, podName, container, path],
    queryFn: () => podListFiles(namespace, podName, container, path, cluster),
    enabled: options?.enabled !== false,
    staleTime: 10000, // 10 seconds cache
  })
}

async function fetchPodMetrics(
  namespace: string,
  podName: string,
  duration: string,
  container?: string,
  labelSelector?: string,
  cluster?: string
): Promise<PodMetrics> {
  let endpoint = `/prometheus/pods/${namespace}/${podName}/metrics?duration=${duration}`
  if (container) {
    endpoint += `&container=${encodeURIComponent(container)}`
  }
  if (labelSelector) {
    endpoint += `&labelSelector=${encodeURIComponent(labelSelector)}`
  }
  return apiClient.get<PodMetrics>(withClusterPath(endpoint, cluster))
}

export const usePodMetrics = (
  namespace: string,
  podName: string,
  duration: string,
  options?: {
    staleTime?: number
    container?: string
    refreshInterval?: number
    labelSelector?: string
    cluster?: string
  }
) => {
  return useQuery({
    queryKey: [
      'pod-metrics',
      namespace,
      podName,
      duration,
      options?.container,
      options?.labelSelector,
      options?.cluster,
    ],
    queryFn: () =>
      fetchPodMetrics(
        namespace,
        podName,
        duration,
        options?.container,
        options?.labelSelector,
        options?.cluster
      ),
    enabled: !!namespace && !!podName,
    staleTime: options?.staleTime || 10000, // 10 seconds cache
    refetchInterval: options?.refreshInterval || 30 * 1000,
    retry: 0,
    placeholderData: (prevData) => prevData,
  })
}

// Mirrors useLogsWebSocket from lib/api/observability.ts with an explicit
// cluster option: the cluster rides the x-cluster-name query param, exactly
// how the upstream hook transports the global cluster.
export const useLogsWebSocket = (
  namespace: string,
  podName: string,
  options?: {
    container?: string
    tailLines?: number
    timestamps?: boolean
    previous?: boolean
    sinceSeconds?: number
    enabled?: boolean
    labelSelector?: string
    cluster?: string
    onNewLog?: (log: string) => void
    onClear?: () => void
  }
) => {
  const { currentCluster } = useCluster()
  const onClear = options?.onClear

  // Build WebSocket URL
  const buildWebSocketUrl = useCallback(() => {
    if (!options?.enabled || !namespace || !podName) return ''

    const params = new URLSearchParams()

    if (options.container) {
      params.append('container', options.container)
    }
    if (options.tailLines !== undefined) {
      params.append('tailLines', options.tailLines.toString())
    }
    if (options.timestamps !== undefined) {
      params.append('timestamps', options.timestamps.toString())
    }
    if (options.previous !== undefined) {
      params.append('previous', options.previous.toString())
    }
    if (options.sinceSeconds !== undefined) {
      params.append('sinceSeconds', options.sinceSeconds.toString())
    }
    if (options.labelSelector) {
      params.append('labelSelector', options.labelSelector)
    }

    appendCurrentClusterParam(params, options?.cluster ?? currentCluster)

    const wsPath = `/api/v1/logs/${namespace}/${podName}/ws?${params.toString()}`
    return getWebSocketUrl(wsPath)
  }, [
    namespace,
    podName,
    options?.container,
    options?.tailLines,
    options?.timestamps,
    options?.previous,
    options?.sinceSeconds,
    options?.enabled,
    options?.labelSelector,
    options?.cluster,
    currentCluster,
  ])

  // WebSocket event handlers
  const handleMessage = useCallback(
    (message: WebSocketMessage) => {
      switch (message.type) {
        case 'log':
          if (message.data && options?.onNewLog) {
            options.onNewLog(message.data)
          }
          break
        case 'error':
          console.error('Log streaming error:', message.data)
          break
        case 'close':
          console.log('Log stream closed:', message.data)
          break
      }
    },
    [options]
  )

  const handleOpen = useCallback(() => {
    console.debug('WebSocket connection opened')
    // Intentionally do NOT call options.onClear here.
    // The historical log buffer (tailLines) is delivered by the server right
    // after the WebSocket opens. Clearing on every onopen races with that
    // delivery and wipes out the tail history (especially on reconnects).
    // The component is responsible for explicitly clearing the editor when
    // the user changes pod / container / tailLines parameters.
  }, [])

  const handleClose = useCallback(() => {
    console.debug('WebSocket connection closed')
  }, [])

  const handleError = useCallback((event: Event) => {
    console.debug('WebSocket error:', event)
  }, [])

  // Use the generic WebSocket hook
  const [wsState, wsActions] = useWebSocket(
    buildWebSocketUrl,
    {
      onMessage: handleMessage,
      onOpen: handleOpen,
      onClose: handleClose,
      onError: handleError,
    },
    {
      enabled: options?.enabled !== false,
      reconnectOnClose: true,
      maxReconnectAttempts: 3,
      reconnectInterval: 5000,
    }
  )

  useEffect(() => {
    onClear?.()
  }, [currentCluster, onClear])

  const refetch = useCallback(() => {
    wsActions.reconnect()
    if (options?.onClear) {
      options.onClear()
    }
  }, [wsActions, options])

  const stopStreaming = useCallback(() => {
    wsActions.disconnect()
  }, [wsActions])

  const clearLogs = useCallback(() => {
    if (options?.onClear) {
      options.onClear()
    }
  }, [options])

  return useMemo(
    () => ({
      isLoading: wsState.isConnecting,
      error: wsState.error,
      isConnected: wsState.isConnected,
      downloadSpeed: wsState.networkStats.downloadSpeed,
      refetch,
      stopStreaming,
      clearLogs,
    }),
    [
      wsState.isConnecting,
      wsState.error,
      wsState.isConnected,
      wsState.networkStats.downloadSpeed,
      refetch,
      stopStreaming,
      clearLogs,
    ]
  )
}
