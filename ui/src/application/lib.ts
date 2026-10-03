// Identity markers and status metadata shared by the application module UI
// (design §5/§3).

import type { ApplicationStatusSummary } from '@/application/types'

import type { Cluster } from '@/types/api'

export const SPACE_LABEL = 'kite.application/space'
export const SPACE_LABEL_SELECTOR = `${SPACE_LABEL}=true`
export const APP_NAME_LABEL = 'kite.application/name'

export type WorkMode = 'application' | 'professional'

export const APPLICATION_STATUSES = [
  'synced',
  'drift',
  'conflict',
  'merged',
  'undeployed',
  'error',
] as const

// Color of a config StatusSummary value; the dot carries the color so the
// list chips, tab dots and overview bar share one rendering (design §11).
export const applicationStatusStyles: Record<
  string,
  { dot: string; chip: string }
> = {
  synced: {
    dot: 'bg-green-500',
    chip: 'border-green-200 bg-green-50 text-green-700 dark:border-green-800 dark:bg-green-950 dark:text-green-400',
  },
  merged: {
    dot: 'bg-sky-500',
    chip: 'border-sky-200 bg-sky-50 text-sky-700 dark:border-sky-800 dark:bg-sky-950 dark:text-sky-400',
  },
  drift: {
    dot: 'bg-amber-500',
    chip: 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-400',
  },
  conflict: {
    dot: 'bg-amber-500',
    chip: 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-400',
  },
  error: {
    dot: 'bg-red-500',
    chip: 'border-red-200 bg-red-50 text-red-700 dark:border-red-800 dark:bg-red-950 dark:text-red-400',
  },
  undeployed: {
    dot: 'bg-zinc-400',
    chip: 'border-zinc-200 bg-zinc-50 text-zinc-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-400',
  },
}

export type TranslateFn = (
  key: string,
  opts?: Record<string, unknown>
) => string

export function applicationStatusLabel(
  status: ApplicationStatusSummary,
  t: TranslateFn
): string {
  return t(`applications.status.${status || 'undeployed'}`)
}

// ── Status dot: cluster health × config status ──
// The dot combines two layers; the cluster layer wins, because a persisted
// "synced" says nothing once the cluster itself is gone or unreachable.

export type ClusterHealth = 'ok' | 'error' | 'missing'

export type ApplicationDotState =
  'synced' | 'attention' | 'error' | 'undeployed' | 'clusterUnreachable'

// Dot color per state. clusterUnreachable renders a red dot on a gray
// backing disc so it stays distinguishable from both the plain config-error
// red and the plain undeployed gray. "attention" is the amber slot for
// every summary that is neither synced/error/undeployed (drift, conflict,
// merged/pending publish); explicit drift detection stays on demand via the
// status refresh / detail-page check, it is not baked into this color.
export const applicationDotStyles: Record<
  ApplicationDotState,
  { backing?: string; dot: string }
> = {
  synced: { dot: 'bg-green-500' },
  attention: { dot: 'bg-amber-500' },
  error: { dot: 'bg-red-500' },
  undeployed: { dot: 'bg-zinc-400' },
  clusterUnreachable: {
    backing: 'bg-zinc-200 dark:bg-zinc-700',
    dot: 'bg-red-500',
  },
}

// clusterHealth resolves the cluster layer from the registered cluster list.
// An absent list (still loading) counts as healthy so dots do not flash the
// unreachable marker during bootstrap.
export function clusterHealth(
  cluster: string,
  clusters: Pick<Cluster, 'name' | 'error'>[] | undefined
): ClusterHealth {
  if (!clusters) return 'ok'
  const registered = clusters.find((candidate) => candidate.name === cluster)
  if (!registered) return 'missing'
  return registered.error ? 'error' : 'ok'
}

export function applicationDotState(
  statusSummary: ApplicationStatusSummary,
  health: ClusterHealth
): ApplicationDotState {
  if (health !== 'ok') return 'clusterUnreachable'
  const status = statusSummary || 'undeployed'
  if (status === 'synced') return 'synced'
  if (status === 'error') return 'error'
  if (status === 'undeployed') return 'undeployed'
  return 'attention'
}

// clusterStatusTooltip builds the dot tooltip: cluster health + config
// status. Without a cluster (never-deployed app) it degrades to the config
// status label.
export function clusterStatusTooltip(
  cluster: string | undefined,
  statusSummary: ApplicationStatusSummary,
  clusters: Pick<Cluster, 'name' | 'error'>[] | undefined,
  t: TranslateFn
): string {
  const status = applicationStatusLabel(statusSummary, t)
  if (!cluster) return status
  const health = clusterHealth(cluster, clusters)
  if (health === 'missing') {
    return `${t('applications.status.clusterMissing')} · ${t('applications.status.configStatus', { status })}`
  }
  if (health === 'error') {
    return `${t('applications.status.clusterError', {
      error:
        clusters?.find((candidate) => candidate.name === cluster)?.error || '',
    })} · ${t('applications.status.configStatus', { status })}`
  }
  return `${t('applications.status.clusterOk')} · ${t('applications.status.configStatus', { status })}`
}
