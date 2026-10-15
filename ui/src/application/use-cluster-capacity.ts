// Cluster capacity reference for the space-quota editors: sums one target
// cluster's node allocatable cpu/memory through the generic nodes API.
import { withClusterPath } from '@/platform/lib/cluster-api'
import { useQuery } from '@tanstack/react-query'
import { Node, NodeList } from 'kubernetes-types/core/v1'

import { apiClient } from '@/lib/api-client'

// "500m" → 0.5, "2" → 2; undefined when the value does not parse.
export function cpuQuantityToCores(q: string | undefined): number | undefined {
  const m = /^(\d*\.?\d+)(m)?$/.exec((q || '').trim())
  if (!m) return undefined
  const n = Number(m[1])
  if (!Number.isFinite(n)) return undefined
  return m[2] === 'm' ? n / 1000 : n
}

// Multipliers to GiB for k8s memory quantity suffixes; a bare number is
// plain bytes.
const MEMORY_GIB_MULTS: Record<string, number> = {
  B: 1 / (1 << 30),
  Ki: 1 / (1 << 20),
  Mi: 1 / (1 << 10),
  Gi: 1,
  Ti: 1 << 10,
  k: 1e3 / (1 << 30),
  K: 1e3 / (1 << 30),
  M: 1e6 / (1 << 30),
  G: 1e9 / (1 << 30),
  T: 1e12 / (1 << 30),
}

// "8Gi" → 8, "512Mi" → 0.5; undefined when the value does not parse.
export function memoryQuantityToGiB(q: string | undefined): number | undefined {
  const m = /^(\d*\.?\d+)(Ki|Mi|Gi|Ti|k|K|M|G|T|B)?$/.exec((q || '').trim())
  if (!m) return undefined
  const n = Number(m[1])
  if (!Number.isFinite(n)) return undefined
  return n * (m[2] ? MEMORY_GIB_MULTS[m[2]] : MEMORY_GIB_MULTS.B)
}

// Quota display rounding: cores with at most 2 decimals, GiB with at most 1.
export function formatCores(n: number): string {
  return String(Number(n.toFixed(2)))
}

export function formatGiB(n: number): string {
  return String(Number(n.toFixed(1)))
}

export interface ClusterCapacity {
  nodes: number
  cpuCores: number
  memoryGiB: number
}

// Read errors (e.g. no nodes/list permission on that cluster) leave the
// query in its error state so callers can hide the reference line instead of
// blocking quota input.
export function useClusterCapacity(cluster: string | undefined) {
  return useQuery({
    queryKey: ['cluster-capacity', cluster],
    queryFn: async (): Promise<ClusterCapacity> => {
      const list = await apiClient.get<NodeList>(
        withClusterPath('/nodes', cluster)
      )
      const items: Node[] = list.items ?? []
      let cpuCores = 0
      let memoryGiB = 0
      for (const node of items) {
        cpuCores += cpuQuantityToCores(node.status?.allocatable?.cpu) ?? 0
        memoryGiB += memoryQuantityToGiB(node.status?.allocatable?.memory) ?? 0
      }
      return { nodes: items.length, cpuCores, memoryGiB }
    },
    enabled: !!cluster,
    staleTime: 60 * 1000,
    retry: false,
  })
}
