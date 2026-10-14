// Identity markers and status metadata shared by the application module UI
// (design §5/§3).

import type { ApplicationStatusSummary } from '@/application/types'

import type { Cluster } from '@/types/api'

export const SPACE_LABEL = 'kite.application/space'
export const SPACE_LABEL_SELECTOR = `${SPACE_LABEL}=true`

// ── Space quota fields (plain-number core / GiB inputs) ──

export interface QuotaFieldValue {
  cpu: string
  memory: string
}

// Both quota inputs must be finite numbers greater than 0; the units are
// fixed (core / GiB).
export function quotaFieldValid(value: QuotaFieldValue | undefined): boolean {
  if (!value) return false
  const cpu = Number(value.cpu)
  const memory = Number(value.memory)
  return (
    Number.isFinite(cpu) && cpu > 0 && Number.isFinite(memory) && memory > 0
  )
}

export type WorkMode = 'application' | 'professional'

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
  drift: {
    dot: 'bg-amber-500',
    chip: 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-400',
  },
  // Saved-but-unpublished edits share drift's amber: both mean "DB and
  // cluster disagree, publish or reconcile", only the direction differs.
  pending: {
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
  'synced' | 'error' | 'undeployed' | 'clusterUnreachable'

// Dot color per state. clusterUnreachable renders a red dot on a gray
// backing disc so it stays distinguishable from both the plain config-error
// red and the plain undeployed gray.
export const applicationDotStyles: Record<
  ApplicationDotState,
  { backing?: string; dot: string }
> = {
  synced: { dot: 'bg-green-500' },
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

// clusterHealthLabel is the cluster layer of the dot tooltips: healthy,
// unreachable with the registration error, or no longer registered.
export function clusterHealthLabel(
  cluster: string,
  clusters: Pick<Cluster, 'name' | 'error'>[] | undefined,
  t: TranslateFn
): string {
  const health = clusterHealth(cluster, clusters)
  if (health === 'missing') return t('applications.status.clusterMissing')
  if (health === 'error') {
    return t('applications.status.clusterError', {
      error:
        clusters?.find((candidate) => candidate.name === cluster)?.error || '',
    })
  }
  return t('applications.status.clusterOk')
}

// ── List status dot: ready-first view ──

export interface ApplicationListStatus {
  state: ApplicationDotState
  /** Dot's companion text: ready/replicas, a status word, or an em dash. */
  text: string
  /** Cluster health · ready replicas · config status. */
  tooltip: string
}

// applicationListStatus derives the list page's ready-first dot for one
// config. The cluster layer still wins: a persisted ready count from an
// unreachable cluster is a stale snapshot, so the dot degrades to the
// unreachable marker and the ready text is replaced by an em dash. Drift
// and space-unlink are detail-page concerns: the overview renders them
// like any other config (ready-first) and keeps only the tooltip note.
export function applicationListStatus(
  config: {
    cluster: string
    statusSummary: ApplicationStatusSummary
    readyReplicas?: number | null
    replicas?: number | null
    spaceUnlinked?: boolean
  },
  clusters: Pick<Cluster, 'name' | 'error'>[] | undefined,
  t: TranslateFn
): ApplicationListStatus {
  const readyText =
    config.readyReplicas == null
      ? null
      : `${config.readyReplicas}/${config.replicas ?? '—'}`
  // An empty summary means "never checked"; the shared label fallback would
  // wrongly call it undeployed, so spell out the unchecked wording here.
  const statusText =
    config.statusSummary === ''
      ? t('applications.list.unchecked')
      : applicationStatusLabel(config.statusSummary, t)
  // One line per layer (space association / cluster health / application
  // readiness+status) so the tooltip does not blend the concerns.
  const tooltip = [
    ...(config.spaceUnlinked
      ? [t('applications.status.spaceUnlinkedNote')]
      : []),
    clusterHealthLabel(config.cluster, clusters, t),
    t('applications.list.readyLabel', { value: readyText ?? '—' }),
    t('applications.status.configStatus', { status: statusText }),
  ].join('\n')

  if (clusterHealth(config.cluster, clusters) !== 'ok') {
    return { state: 'clusterUnreachable', text: '—', tooltip }
  }
  if (config.statusSummary === 'undeployed') {
    return {
      state: 'undeployed',
      text: t('applications.status.undeployed'),
      tooltip,
    }
  }
  if (config.statusSummary === '') {
    return {
      state: 'undeployed',
      text: t('applications.list.unchecked'),
      tooltip,
    }
  }
  if (config.statusSummary === 'error') {
    return { state: 'error', text: t('applications.status.error'), tooltip }
  }
  if (config.readyReplicas != null) {
    // Both fields are written together by the status check; a missing
    // replicas degrades to "ready" rather than claiming not-ready.
    const ready =
      config.replicas == null || config.readyReplicas >= config.replicas
    return {
      state: ready ? 'synced' : 'error',
      text: `${config.readyReplicas}/${config.replicas ?? '—'}`,
      tooltip,
    }
  }
  // Checked configs without replica data (drift without a readable
  // workload) render like synced: no signal at the overview, the persisted
  // status stays in the tooltip and the detail page owns the drift surface.
  return { state: 'synced', text: '—', tooltip }
}

// ── Drift path values (in-diff side-pick widgets) ──

export interface DocYamlDump {
  text: string
  /** Changed-path → 1-based line number in text; only leaves are recorded. */
  lineOfPath: Record<string, number>
  /** Leaf paths in document order; anchors for paths missing on this side. */
  orderedPaths: string[]
}

// Strings safe to render as YAML plain scalars: restricted charset, no
// leading digit and no bool/null word, so the value cannot be re-read as
// another scalar type. Everything else keeps JSON quoting (valid YAML
// double-quote style).
const PLAIN_STRING = /^[A-Za-z_][A-Za-z0-9\-_/.]*$/
const PLAIN_RESERVED = new Set([
  'true',
  'false',
  'null',
  'yes',
  'no',
  'on',
  'off',
  'y',
  'n',
  '~',
])

// dumpDocYaml renders one normalized document as YAML while recording the
// line of every leaf path, using the server's path spelling (dot keys,
// [name=x] selectors on fully-named lists, [N] otherwise) so status-report
// diff paths resolve to exact lines. Deterministic sorted keys — both diff
// sides go through it, so Monaco sees consistent formatting.
export function dumpDocYaml(value: unknown): DocYamlDump {
  const lines: string[] = []
  const lineOfPath: Record<string, number> = {}
  const orderedPaths: string[] = []
  const isObj = (v: unknown): v is Record<string, unknown> =>
    v !== null && typeof v === 'object' && !Array.isArray(v)
  const scalar = (v: unknown): string => {
    if (v === undefined) return 'null'
    if (typeof v === 'string') {
      if (PLAIN_STRING.test(v) && !PLAIN_RESERVED.has(v.toLowerCase())) {
        return v
      }
      return JSON.stringify(v)
    }
    return String(v)
  }
  const sorted = (map: Record<string, unknown>) =>
    Object.entries(map).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
  const selector = (list: unknown[], index: number): string => {
    const item = list[index]
    if (
      isObj(item) &&
      typeof item.name === 'string' &&
      item.name !== '' &&
      list.every((e) => isObj(e) && typeof e.name === 'string' && e.name !== '')
    ) {
      return `name=${item.name}`
    }
    return String(index)
  }

  // emitMap writes one map's keys at keyIndent; dash carries the " - " prefix
  // of a list-item map, so its first key shares the dash line and the rest
  // align two columns deeper.
  const emitMap = (
    map: Record<string, unknown>,
    path: string,
    keyIndent: number,
    dash?: string
  ) => {
    const entries = sorted(map)
    if (entries.length === 0) {
      lines.push(`${dash ?? ' '.repeat(keyIndent)}{}`)
      return
    }
    const pad = ' '.repeat(keyIndent + (dash !== undefined ? 2 : 0))
    entries.forEach(([key, child], index) => {
      const childPath = path ? `${path}.${key}` : key
      const prefix = index === 0 && dash !== undefined ? dash : pad
      if (isObj(child)) {
        if (Object.keys(child).length === 0) {
          lines.push(`${prefix}${key}: {}`)
          return
        }
        lines.push(`${prefix}${key}:`)
        emitMap(child, childPath, keyIndent + 2)
      } else if (Array.isArray(child)) {
        if (child.length === 0) {
          lines.push(`${prefix}${key}: []`)
          return
        }
        lines.push(`${prefix}${key}:`)
        emitList(child, childPath, keyIndent + 2)
      } else {
        lines.push(`${prefix}${key}: ${scalar(child)}`)
        lineOfPath[childPath] = lines.length
        orderedPaths.push(childPath)
      }
    })
  }
  const emitList = (list: unknown[], path: string, itemIndent: number) => {
    list.forEach((item, index) => {
      const childPath = `${path}[${selector(list, index)}]`
      const dash = `${' '.repeat(itemIndent)}- `
      if (isObj(item)) {
        emitMap(item, childPath, itemIndent, dash)
      } else if (Array.isArray(item)) {
        lines.push(dash.trimEnd())
        emitList(item, childPath, itemIndent + 2)
      } else {
        lines.push(`${dash}${scalar(item)}`)
        lineOfPath[childPath] = lines.length
        orderedPaths.push(childPath)
      }
    })
  }

  if (isObj(value)) emitMap(value, '', 0)
  else if (Array.isArray(value)) emitList(value, '', 0)
  else if (value !== undefined) lines.push(scalar(value))
  return { text: lines.join('\n'), lineOfPath, orderedPaths }
}
