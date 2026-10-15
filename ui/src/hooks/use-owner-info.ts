import { useQuery } from '@tanstack/react-query'
import type { ObjectMeta } from 'kubernetes-types/meta/v1'

import { fetchAPI } from '@/lib/api/shared'
import { withCurrentClusterPath } from '@/lib/current-cluster'
import { getCRDResourcePath, isStandardK8sResource } from '@/lib/k8s'
import {
  getResourceDetailPath,
  getResourceMetadata,
} from '@/lib/resource-catalog'

import { useCluster } from './use-cluster'

export function useOwnerInfo(metadata?: ObjectMeta) {
  const owner = metadata?.ownerReferences?.[0]
  const standardType =
    owner && isStandardK8sResource(owner.kind)
      ? getResourceMetadata(owner.kind)?.type
      : undefined
  const { currentCluster } = useCluster()
  const { data: ownerResource } = useQuery({
    queryKey: [
      'owner-resource',
      currentCluster,
      owner?.apiVersion,
      owner?.kind,
      metadata?.namespace,
    ],
    queryFn: () => {
      const params = new URLSearchParams({
        apiVersion: owner!.apiVersion,
        kind: owner!.kind,
      })
      if (metadata?.namespace) params.set('namespace', metadata.namespace)
      return fetchAPI<{
        resource: string
        scope: 'Namespaced' | 'Cluster'
      }>(withCurrentClusterPath(`/resources/resolve?${params}`, currentCluster))
    },
    enabled: !!owner && !standardType && !!currentCluster,
    staleTime: 5 * 60 * 1000,
  })

  if (!owner) return null

  const path = standardType
    ? getResourceDetailPath(standardType, owner.name, metadata?.namespace)
    : ownerResource
      ? getCRDResourcePath(
          ownerResource.resource,
          owner.apiVersion,
          ownerResource.scope === 'Namespaced'
            ? metadata?.namespace
            : undefined,
          owner.name
        )
      : undefined

  return {
    kind: owner.kind,
    name: owner.name,
    path,
    controller: owner.controller || false,
  }
}
