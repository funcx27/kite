// Run view (design §11 视图③): read-only canvas colored by NodeRun status,
// bottom log drawer fed by LogTail + the SSE stream (GET /runs/:id/events
// with auto reconnect), inline approval actions with comment, cancel/retry,
// and a run-level event feed from the same stream.
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useParams } from 'react-router-dom'
import { toast } from 'sonner'

import { formatDate } from '@/lib/utils'
import { usePageTitle } from '@/hooks/use-page-title'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'

import {
  useDecideApprovalMutation,
  useInvalidateWorkflow,
  useRun,
  useRunActionMutation,
} from '../api'
import { FlowCanvas } from '../components/flow'
import { LogPane } from '../components/log-pane'
import { NodeRunStatusChip, RunStatusChip } from '../components/status-chip'
import { WORKFLOW_NODE_CARD, type WorkflowFlowNode } from '../flow-model'
import { useRunEventStream } from '../hooks/use-run-stream'
import {
  autoLayout,
  formatDuration,
  isRetryableRunStatus,
  isTerminalRunStatus,
  nodeLayout,
} from '../lib'
import type { NodeRunStatus, RunNodeView } from '../types'

export function WorkflowRunPage() {
  const { name = '', runId } = useParams()
  const runIdNumber = Number(runId)
  const { t } = useTranslation()
  const invalidate = useInvalidateWorkflow()
  const {
    data: run,
    isLoading,
    error,
  } = useRun(Number.isFinite(runIdNumber) ? runIdNumber : undefined)

  usePageTitle(t('workflow.run.title', { n: run?.runNumber ?? runId ?? '' }))

  // The stream overlays fetched state; `done` (and any local action) refreshes
  // the run detail so outputs/logTail catch up. Bumping streamNonce re-opens
  // the stream and drops the terminal overlay after a retry/cancel re-drives
  // an ended run — without it the done-time statuses would pin the view.
  const [streamNonce, setStreamNonce] = useState(0)
  const stream = useRunEventStream(run?.id, () => invalidate(), streamNonce)

  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const { cancel, retry } = useRunActionMutation()
  const decide = useDecideApprovalMutation(runIdNumber)

  const nodeStatus = (nodeId: string): NodeRunStatus | undefined =>
    stream.nodeStatuses[nodeId] ??
    run?.nodes.find((n) => n.nodeId === nodeId)?.status

  const runStatus = stream.runStatus ?? run?.status

  const canvasNodes = useMemo<WorkflowFlowNode[]>(() => {
    if (!run) return []
    const positions = new Map<string, { x: number; y: number }>()
    for (const n of run.graph.nodes) {
      const layout = nodeLayout(n.props)
      if (layout) positions.set(n.id, layout)
    }
    if (positions.size < run.graph.nodes.length) {
      for (const [id, p] of autoLayout(run.graph)) {
        if (!positions.has(id)) positions.set(id, p)
      }
    }
    return run.graph.nodes.map((n) => {
      const status =
        stream.nodeStatuses[n.id] ??
        run.nodes.find((v) => v.nodeId === n.id)?.status
      return {
        id: n.id,
        type: WORKFLOW_NODE_CARD,
        position: positions.get(n.id) ?? { x: 40, y: 40 },
        data: {
          nodeType: n.type,
          label: n.name,
          status,
          waitingApproval: status === 'running' && n.type === 'approval',
        },
      }
    })
  }, [run, stream.nodeStatuses])

  const canvasEdges = useMemo(
    () =>
      (run?.graph.edges ?? []).map((e) => ({
        id: `${e.from}->${e.to}`,
        source: e.from,
        target: e.to,
      })),
    [run]
  )

  const selectedRunNode: RunNodeView | null =
    (selectedNodeId && run?.nodes.find((n) => n.nodeId === selectedNodeId)) ||
    null

  const waitingApprovals = (run?.nodes ?? []).filter(
    (n) =>
      n.type === 'approval' &&
      (stream.nodeStatuses[n.nodeId] ?? n.status) === 'running'
  )

  const mergedLogText = (node: RunNodeView): string => {
    const streamed = stream.logs[node.nodeId] ?? []
    if (streamed.length > 0) return streamed.join('\n')
    return node.logTail ?? ''
  }

  if (error) {
    return (
      <div className="py-10 text-center text-sm text-destructive">
        {(error as Error).message}
      </div>
    )
  }
  if (isLoading || !run) {
    return (
      <div className="py-10 text-center text-sm text-muted-foreground">
        {t('common.messages.loading')}
      </div>
    )
  }

  return (
    <div className="flex h-[calc(100dvh-11rem)] flex-col gap-3">
      {/* Run header */}
      <div className="flex flex-wrap items-center gap-2.5">
        <Link
          to={`/workflows/${name}/runs`}
          className="text-sm font-semibold hover:underline"
        >
          {name}
        </Link>
        <RunStatusChip status={runStatus ?? run.status} />
        <span className="font-mono text-xs text-muted-foreground">
          #{run.runNumber} · rev {run.revisionRev}
        </span>
        <span className="text-xs text-muted-foreground">
          {t('workflow.list.trigger' + capitalize(run.triggerType))}
          {run.triggeredByName ? ` · ${run.triggeredByName}` : ''}
          {run.triggerRef ? ` · ${run.triggerRef}` : ''}
        </span>
        <span className="text-xs text-muted-foreground">
          {(run.startedAt && formatDate(run.startedAt)) || '—'} ·{' '}
          {formatDuration(run.startedAt, run.finishedAt)}
        </span>
        {Object.entries(run.params ?? {}).map(([key, value]) => (
          <Badge
            key={key}
            variant="secondary"
            className="font-mono text-xs"
            title={key}
          >
            {key}={String(value)}
          </Badge>
        ))}
        <div className="ml-auto flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={cancel.isPending || isTerminalRunStatus(runStatus)}
            onClick={() =>
              cancel.mutate(run.id, {
                onSuccess: () => {
                  setStreamNonce((n) => n + 1)
                  toast.success(t('workflow.run.cancelConfirmed'))
                },
                onError: (err) => toast.error(err.message),
              })
            }
          >
            {t('workflow.run.cancel')}
          </Button>
          <Button
            variant="outline"
            size="sm"
            title={
              !isRetryableRunStatus(runStatus)
                ? t('workflow.run.retryDisabledHint')
                : undefined
            }
            disabled={!isRetryableRunStatus(runStatus) || retry.isPending}
            onClick={() =>
              retry.mutate(run.id, {
                onSuccess: () => {
                  // Re-open SSE and drop the terminal overlay so the view
                  // follows the re-drived run immediately.
                  setStreamNonce((n) => n + 1)
                  toast.success(t('workflow.run.retryConfirmed'))
                },
                onError: (err) => toast.error(err.message),
              })
            }
          >
            ⟳ {t('workflow.run.retry')}
          </Button>
        </div>
      </div>

      {/* Canvas + overlays */}
      <div className="relative min-h-0 flex-1 overflow-hidden rounded-lg border">
        <FlowCanvas
          nodes={canvasNodes}
          edges={canvasEdges}
          readOnly
          onSelectNode={setSelectedNodeId}
        />

        {/* Legend */}
        <div className="absolute left-3 top-3 flex flex-col gap-1 rounded-lg border bg-background/90 px-3 py-2 text-[11px] shadow-sm">
          <div className="font-medium text-muted-foreground">
            {t('workflow.run.legend')}
          </div>
          <LegendRow
            color="bg-green-500"
            label={t('workflow.statuses.succeeded')}
          />
          <LegendRow
            color="bg-blue-500"
            label={t('workflow.statuses.running')}
          />
          <LegendRow
            color="bg-amber-500"
            label={t('workflow.statuses.waitingApproval')}
          />
          <LegendRow color="bg-red-500" label={t('workflow.statuses.failed')} />
          <LegendRow
            color="bg-zinc-300"
            label={t('workflow.statuses.pending')}
          />
          <LegendRow
            color="bg-zinc-400"
            label={t('workflow.statuses.skipped')}
          />
        </div>

        {/* Stream state + run-level event feed */}
        <div className="absolute right-3 top-3 max-h-56 w-72 overflow-hidden rounded-lg border bg-background/90 shadow-sm">
          <div className="flex items-center justify-between border-b px-3 py-1.5 text-[11px]">
            <span className="font-medium text-muted-foreground">
              {t('workflow.run.eventsTitle')}
            </span>
            <span className="flex items-center gap-1 text-muted-foreground">
              <span
                className={`inline-block h-1.5 w-1.5 rounded-full ${stream.connected ? 'bg-green-500' : stream.ended ? 'bg-zinc-400' : 'bg-amber-500 animate-pulse'}`}
              />
              {stream.connected
                ? t('workflow.run.live')
                : stream.ended
                  ? t('workflow.run.ended')
                  : t('workflow.run.reconnecting')}
            </span>
          </div>
          <div className="max-h-40 overflow-y-auto px-3 py-1.5 font-mono text-[11px] leading-4 text-muted-foreground">
            {stream.events.length === 0 ? (
              <p>{t('workflow.run.eventsEmpty')}</p>
            ) : (
              stream.events.map((event, index) => (
                <div key={index}>{event}</div>
              ))
            )}
          </div>
        </div>

        {/* Waiting approvals */}
        {waitingApprovals.length > 0 && (
          <div className="absolute bottom-3 left-3 flex flex-col gap-2">
            {waitingApprovals.map((node) => (
              <ApprovalCard
                key={node.nodeId}
                node={node}
                deciding={decide.isPending}
                onDecide={(approve, comment) =>
                  decide.mutate(
                    { nodeId: node.nodeId, approve, comment },
                    {
                      onSuccess: () =>
                        toast.success(
                          approve
                            ? t('workflow.run.approved')
                            : t('workflow.run.rejected')
                        ),
                      onError: (err) =>
                        toast.error(
                          err instanceof Error &&
                            err.message.includes('assignees')
                            ? t('workflow.run.decideForbidden')
                            : err.message
                        ),
                    }
                  )
                }
              />
            ))}
          </div>
        )}
      </div>

      {/* Node log drawer */}
      <Sheet
        open={selectedNodeId !== null}
        onOpenChange={(open) => !open && setSelectedNodeId(null)}
      >
        <SheetContent
          side="bottom"
          className="h-[60dvh] gap-3 p-4 sm:max-w-none"
        >
          <SheetHeader className="border-b pb-3">
            <SheetTitle className="flex flex-wrap items-center gap-2 text-sm">
              {t('workflow.run.logTitle')}
              {selectedRunNode && (
                <>
                  <span className="font-mono">{selectedRunNode.nodeId}</span>
                  <NodeRunStatusChip
                    status={
                      nodeStatus(selectedRunNode.nodeId) ??
                      selectedRunNode.status
                    }
                    nodeType={selectedRunNode.type}
                  />
                  {selectedRunNode.attempt !== undefined &&
                    selectedRunNode.attempt > 1 && (
                      <Badge variant="outline" className="font-mono text-xs">
                        {t('workflow.run.attempt', {
                          n: selectedRunNode.attempt,
                        })}
                      </Badge>
                    )}
                  <span className="text-xs text-muted-foreground">
                    {formatDuration(
                      selectedRunNode.startedAt,
                      selectedRunNode.finishedAt
                    )}
                  </span>
                </>
              )}
              {stream.connected && (
                <span className="ml-auto flex items-center gap-1 text-xs text-muted-foreground">
                  <span className="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-green-500" />
                  SSE {t('workflow.run.live')}
                </span>
              )}
            </SheetTitle>
          </SheetHeader>
          {selectedRunNode && (
            <div className="flex min-h-0 flex-1 flex-col gap-3">
              <NodeMeta node={selectedRunNode} />
              <div className="min-h-0 flex-1">
                <LogPane text={mergedLogText(selectedRunNode)} />
              </div>
            </div>
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}

function capitalize(value: string): string {
  return value.charAt(0).toUpperCase() + value.slice(1)
}

function LegendRow({ color, label }: { color: string; label: string }) {
  return (
    <div className="flex items-center gap-1.5">
      <span className={`inline-block h-2 w-2 rounded-full ${color}`} />
      {label}
    </div>
  )
}

function ApprovalCard({
  node,
  onDecide,
  deciding,
}: {
  node: RunNodeView
  onDecide: (approve: boolean, comment: string) => void
  deciding: boolean
}) {
  const { t } = useTranslation()
  const [comment, setComment] = useState('')
  return (
    <div className="w-96 rounded-lg border border-amber-300 bg-amber-50/95 p-3 shadow-sm dark:border-amber-800 dark:bg-amber-950/80">
      <div className="mb-2 text-xs font-medium text-amber-700 dark:text-amber-300">
        ⏸ {t('workflow.run.waitingBanner', { node: node.name || node.nodeId })}
      </div>
      <div className="flex items-center gap-2">
        <Input
          value={comment}
          onChange={(e) => setComment(e.target.value)}
          placeholder={t('workflow.run.commentPlaceholder')}
          className="h-8 flex-1 bg-background text-xs"
        />
        <Button
          size="sm"
          className="h-8"
          disabled={deciding}
          onClick={() => onDecide(true, comment)}
        >
          {t('workflow.run.approve')}
        </Button>
        <Button
          size="sm"
          variant="destructive"
          className="h-8"
          disabled={deciding}
          onClick={() => onDecide(false, comment)}
        >
          {t('workflow.run.reject')}
        </Button>
      </div>
    </div>
  )
}

// Node summary between drawer header and logs: error and outputs (inputs are
// the rendered props — useful when a template misfired).
function NodeMeta({ node }: { node: RunNodeView }) {
  const { t } = useTranslation()
  return (
    <div className="space-y-2">
      {node.error && (
        <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
          <span className="font-medium">{t('workflow.run.error')}: </span>
          {node.error}
        </div>
      )}
      {node.outputs && Object.keys(node.outputs).length > 0 && (
        <div className="rounded-md border px-3 py-2 text-xs">
          <span className="font-medium text-muted-foreground">
            {t('workflow.run.outputs')}:{' '}
          </span>
          <code className="break-all font-mono">
            {JSON.stringify(node.outputs)}
          </code>
        </div>
      )}
    </div>
  )
}
