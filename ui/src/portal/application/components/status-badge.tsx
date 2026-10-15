import type { ReactNode } from 'react'
import {
  applicationDotStyles,
  applicationListStatus,
  applicationStatusLabel,
  applicationStatusStyles,
  type ApplicationDotState,
} from '@/portal/application/lib'
import {
  ApplicationConfigSummary,
  ApplicationStatusSummary,
} from '@/portal/application/types'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'
import { useCluster } from '@/hooks/use-cluster'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

// NoteTooltip renders the layered status notes (space association / cluster
// health / config status) as a real multi-line tooltip; native title
// attributes swallow the newlines and collapse everything onto one line.
function NoteTooltip({
  note,
  children,
}: {
  note?: string
  children: ReactNode
}) {
  if (!note) return <>{children}</>
  return (
    <Tooltip>
      <TooltipTrigger asChild>{children}</TooltipTrigger>
      <TooltipContent className="whitespace-pre-line text-left">
        {note}
      </TooltipContent>
    </Tooltip>
  )
}

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
// lookup; shared by the list status column and its legend. The
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
      <NoteTooltip note={title}>
        <span
          className={cn(
            'inline-flex h-2.5 w-2.5 shrink-0 items-center justify-center rounded-full',
            style.backing,
            className
          )}
        >
          <span className={cn('h-1.5 w-1.5 rounded-full', style.dot)} />
        </span>
      </NoteTooltip>
    )
  }
  return (
    <NoteTooltip note={title}>
      <span
        className={cn(
          'inline-block h-2 w-2 rounded-full',
          style.dot,
          className
        )}
      />
    </NoteTooltip>
  )
}

// ApplicationConfigStatus renders one config's row in the list page status
// column: ready-first dot + ready/replicas text + cluster name. Pure display
// (cluster filtering lives in the toolbar); the title tooltip spells out
// cluster health, ready replicas and config status.
export function ApplicationConfigStatus({
  config,
}: {
  config: ApplicationConfigSummary
}) {
  const { t } = useTranslation()
  const { clusters, isLoading: clustersLoading } = useCluster()
  const knownClusters = clustersLoading ? undefined : clusters
  const { state, text, tooltip } = applicationListStatus(
    config,
    knownClusters,
    t
  )
  return (
    <NoteTooltip note={tooltip}>
      <span className="flex items-center gap-1.5 whitespace-nowrap">
        <StatusDotGlyph state={state} />
        {text && <span className="font-mono text-xs tabular-nums">{text}</span>}
        <span className="font-mono text-xs text-muted-foreground">
          {config.cluster}
        </span>
      </span>
    </NoteTooltip>
  )
}
