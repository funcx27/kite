// Shared module helpers: node-type metadata, run-status colors, DAG layout
// and the client-side acyclicity check shown before save (the server re-checks).
import type {
  DAGSpec,
  NodeRunStatus,
  ParamSpec,
  RunStatus,
  WorkflowNode,
  WorkflowNodeType,
} from './types'

export const WORKFLOW_NODE_TYPES: WorkflowNodeType[] = [
  'build-image',
  'deploy-app',
  'approval',
  'notify',
]

// Tailwind classes per node type (editor card accent + palette icon).
export const nodeTypeAccent: Record<WorkflowNodeType, string> = {
  'build-image': 'bg-zinc-900',
  'deploy-app': 'bg-emerald-600',
  approval: 'bg-amber-500',
  notify: 'bg-sky-600',
}

// Node run status → dot/border color. Approval nodes paint "running" as
// waiting-approval amber (design §11 着色口径).
export function nodeStatusColor(
  status: NodeRunStatus | undefined,
  type?: string
): string {
  switch (status) {
    case 'succeeded':
      return 'bg-green-500'
    case 'failed':
      return 'bg-red-500'
    case 'running':
      return type === 'approval' ? 'bg-amber-500' : 'bg-blue-500'
    case 'pending':
      return 'bg-zinc-300'
    case 'skipped':
      return 'bg-zinc-400'
    case 'cancelled':
      return 'bg-zinc-500'
    case 'interrupted':
      return 'bg-orange-500'
    default:
      return 'bg-zinc-300'
  }
}

export const runStatusColor: Record<RunStatus, string> = {
  queued: 'bg-zinc-400',
  running: 'bg-blue-500',
  succeeded: 'bg-green-500',
  failed: 'bg-red-500',
  cancelled: 'bg-zinc-500',
}

export function isTerminalRunStatus(status: RunStatus | undefined): boolean {
  return status === 'succeeded' || status === 'failed' || status === 'cancelled'
}

// Retry gate mirrors the engine (actions.go Retry): only failed/cancelled
// runs accept POST /runs/:id/retry.
export function isRetryableRunStatus(status: RunStatus | undefined): boolean {
  return status === 'failed' || status === 'cancelled'
}

export function formatDuration(
  startedAt?: string | null,
  finishedAt?: string | null
): string {
  if (!startedAt) return '—'
  const end = finishedAt ? new Date(finishedAt).getTime() : Date.now()
  const ms = Math.max(0, end - new Date(startedAt).getTime())
  const seconds = Math.floor(ms / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`
  const hours = Math.floor(minutes / 60)
  return `${hours}h ${minutes % 60}m`
}

// findCycle returns the first cycle as a path ("a -> b -> a"), or null when
// the graph is acyclic. Same rule as the server's DFS check.
export function findCycle(spec: DAGSpec): string[] | null {
  const adj = new Map<string, string[]>()
  for (const e of spec.edges) {
    adj.set(e.from, [...(adj.get(e.from) ?? []), e.to])
  }
  const WHITE = 0
  const GRAY = 1
  const BLACK = 2
  const color = new Map<string, number>()
  let stack: string[] = []
  let cycle: string[] | null = null
  const visit = (id: string) => {
    if (cycle) return
    color.set(id, GRAY)
    stack.push(id)
    for (const next of adj.get(id) ?? []) {
      const c = color.get(next) ?? WHITE
      if (c === GRAY) {
        const start = stack.indexOf(next)
        cycle = [...stack.slice(start), next]
        return
      }
      if (c === WHITE) visit(next)
      if (cycle) return
    }
    stack = stack.slice(0, -1)
    color.set(id, BLACK)
  }
  for (const n of spec.nodes) {
    if ((color.get(n.id) ?? WHITE) === WHITE) visit(n.id)
    if (cycle) break
  }
  return cycle
}

export interface NodeLayout {
  x: number
  y: number
}

// Read-back of props.layout written by an earlier editor build. The engine's
// decodeProps rejects unknown props fields, so layout is no longer persisted
// — this only keeps legacy revisions rendering where they were drawn.
export function nodeLayout(
  props: Record<string, unknown> | undefined
): NodeLayout | null {
  const layout = props?.layout as NodeLayout | undefined
  if (
    layout &&
    typeof layout.x === 'number' &&
    Number.isFinite(layout.x) &&
    typeof layout.y === 'number' &&
    Number.isFinite(layout.y)
  ) {
    return layout
  }
  return null
}

const COLUMN_WIDTH = 280
const ROW_HEIGHT = 130
const ORIGIN = 40

// Canonical layout: x by longest-path depth, y by row order. Deterministic,
// so editor and run view agree without persisting positions anywhere.
export function autoLayout(spec: DAGSpec): Map<string, NodeLayout> {
  const incoming = new Map<string, number>()
  const adj = new Map<string, string[]>()
  for (const n of spec.nodes) {
    incoming.set(n.id, 0)
  }
  for (const e of spec.edges) {
    if (incoming.has(e.to) && incoming.has(e.from)) {
      incoming.set(e.to, (incoming.get(e.to) ?? 0) + 1)
      adj.set(e.from, [...(adj.get(e.from) ?? []), e.to])
    }
  }
  const depth = new Map<string, number>()
  const queue = spec.nodes
    .filter((n) => (incoming.get(n.id) ?? 0) === 0)
    .map((n) => n.id)
  for (const id of queue) depth.set(id, 0)
  while (queue.length > 0) {
    const id = queue.shift() as string
    for (const next of adj.get(id) ?? []) {
      depth.set(next, Math.max(depth.get(next) ?? 0, (depth.get(id) ?? 0) + 1))
      const remaining = (incoming.get(next) ?? 1) - 1
      incoming.set(next, remaining)
      if (remaining === 0) queue.push(next)
    }
  }
  const positions = new Map<string, NodeLayout>()
  const rows = new Map<number, number>()
  for (const n of spec.nodes) {
    const layer = depth.get(n.id) ?? 0
    const row = rows.get(layer) ?? 0
    rows.set(layer, row + 1)
    positions.set(n.id, {
      x: ORIGIN + layer * COLUMN_WIDTH,
      y: ORIGIN + row * ROW_HEIGHT,
    })
  }
  return positions
}

// Node ids surface in template paths (steps.<id>.outputs.<key>), so restrict
// them to the template-safe charset; uniqueness is enforced by the caller.
export function isValidNodeNodeId(id: string): boolean {
  return /^[A-Za-z0-9_]+$/.test(id)
}

// Known output keys per node type, feeding the steps.* template refs
// (mirrors the runners' outputs in engine/runner.go).
export const outputKeysByType: Record<WorkflowNodeType, string[]> = {
  'build-image': ['imageRef', 'digest', 'imageRefWithDigest', 'commit'],
  'deploy-app': ['revision', 'preview'],
  approval: [],
  notify: ['delivered'],
}

// Template refs offered on a node's fields (autocomplete options): the
// workflow params, the run fields and the outputs of the nodes that can
// finish before it runs — itself and its DAG descendants excluded, since the
// runtime only resolves steps.* against already-succeeded nodes.
export function buildTemplateRefs(
  params: ParamSpec[],
  nodes: WorkflowNode[],
  currentNodeId?: string,
  gitScope?: boolean,
  edges: DAGSpec['edges'] = []
): string[] {
  const refs = params.map((p) => `params.${p.name}`)
  if (gitScope) {
    refs.push('git.commit', 'git.shortCommit')
  }
  refs.push('run.number', 'run.status', 'run.url')
  const unreachable = new Set<string>()
  if (currentNodeId) {
    unreachable.add(currentNodeId)
    const queue = [currentNodeId]
    while (queue.length > 0) {
      const cur = queue.shift() as string
      for (const e of edges) {
        if (e.from === cur && !unreachable.has(e.to)) {
          unreachable.add(e.to)
          queue.push(e.to)
        }
      }
    }
  }
  for (const other of nodes) {
    if (unreachable.has(other.id)) continue
    for (const key of outputKeysByType[other.type] ?? []) {
      refs.push(`steps.${other.id}.outputs.${key}`)
    }
  }
  return refs
}
