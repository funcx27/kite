// Status chips for run / node states, colored per design §11.
import { useTranslation } from 'react-i18next'

import { nodeStatusColor, runStatusColor } from '../lib'
import type { NodeRunStatus, RunStatus } from '../types'

export function RunStatusChip({
  status,
  failedNodes = 0,
}: {
  status: RunStatus
  failedNodes?: number
}) {
  const { t } = useTranslation()
  // A succeeded run with failed nodes ran them under continue semantics
  // (notify or onFailure=continue): amber instead of plain green, so the
  // "best-effort" failure stays visible at list level.
  const warning = status === 'succeeded' && failedNodes > 0
  return (
    <span
      className="inline-flex items-center gap-1.5 rounded-full border bg-muted/40 px-2 py-0.5 text-xs"
      title={
        warning
          ? t('workflow.statuses.warningTooltip', { count: failedNodes })
          : undefined
      }
    >
      <span
        className={`inline-block h-2 w-2 rounded-full ${
          warning ? 'bg-amber-500' : (runStatusColor[status] ?? 'bg-zinc-400')
        } ${status === 'running' || status === 'queued' ? 'animate-pulse' : ''}`}
      />
      {warning
        ? t('workflow.statuses.succeededWithWarnings')
        : t(`workflow.statuses.${status}`)}
    </span>
  )
}

export function NodeRunStatusChip({
  status,
  nodeType,
}: {
  status: NodeRunStatus
  nodeType?: string
}) {
  const { t } = useTranslation()
  // A running approval paints as waiting-approval (design §11 黄=等待审批).
  const label =
    status === 'running' && nodeType === 'approval'
      ? t('workflow.statuses.waitingApproval')
      : t(`workflow.statuses.${status}`)
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full border bg-muted/40 px-2 py-0.5 text-xs">
      <span
        className={`inline-block h-2 w-2 rounded-full ${nodeStatusColor(status, nodeType)} ${
          status === 'running' ? 'animate-pulse' : ''
        }`}
      />
      {label}
    </span>
  )
}
