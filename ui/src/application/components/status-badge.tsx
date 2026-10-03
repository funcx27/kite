import {
  applicationDotState,
  applicationDotStyles,
  applicationStatusLabel,
  applicationStatusStyles,
  clusterHealth,
  clusterStatusTooltip,
  type ApplicationDotState,
} from '@/application/lib'
import { ApplicationStatusSummary } from '@/application/types'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'
import { useCluster } from '@/hooks/use-cluster'

export function ApplicationStatusChip({
  status,
  className,
}: {
  status: ApplicationStatusSummary
  className?: string
}) {
  const { t } = useTranslation()
  const style = applicationStatusStyles[status || 'undeployed']
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 whitespace-nowrap rounded-full border px-2 py-0.5 text-xs font-medium',
        style?.chip,
        className
      )}
    >
      <span className={cn('h-2 w-2 shrink-0 rounded-full', style?.dot)} />
      {applicationStatusLabel(status, t)}
    </span>
  )
}

// StatusDotGlyph renders the bare dot shape for a dot state — no cluster
// lookup; shared by ClusterStatusDot and the list legend. The
// clusterUnreachable state is a red dot on a gray backing disc so it stays
// distinguishable from the plain config-error red and undeployed gray.
export function StatusDotGlyph({
  state,
  className,
  title,
}: {
  state: ApplicationDotState
  className?: string
  title?: string
}) {
  const style = applicationDotStyles[state]
  if (style.backing) {
    return (
      <span
        title={title}
        className={cn(
          'inline-flex h-2.5 w-2.5 shrink-0 items-center justify-center rounded-full',
          style.backing,
          className
        )}
      >
        <span className={cn('h-1.5 w-1.5 rounded-full', style.dot)} />
      </span>
    )
  }
  return (
    <span
      title={title}
      className={cn('inline-block h-2 w-2 rounded-full', style.dot, className)}
    />
  )
}

// ClusterStatusDot renders the shared cluster-health × config-status dot
// (detail-page cluster tabs and the list status column). The cluster layer
// wins over the persisted config summary; the title tooltip spells out both
// layers.
export function ClusterStatusDot({
  cluster,
  statusSummary,
  className,
}: {
  cluster?: string
  statusSummary: ApplicationStatusSummary
  className?: string
}) {
  const { t } = useTranslation()
  const { clusters, isLoading: clustersLoading } = useCluster()
  // While the cluster list is still loading it cannot judge health; pass
  // undefined so dots do not flash the unreachable marker on bootstrap.
  const knownClusters = clustersLoading ? undefined : clusters
  const state = applicationDotState(
    statusSummary,
    clusterHealth(cluster ?? '', cluster ? knownClusters : undefined)
  )
  return (
    <StatusDotGlyph
      state={state}
      title={clusterStatusTooltip(cluster, statusSummary, knownClusters, t)}
      className={className}
    />
  )
}
