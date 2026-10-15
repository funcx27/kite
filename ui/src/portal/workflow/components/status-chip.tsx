// Status chips for run / node states, colored per design §11.
import { useTranslation } from 'react-i18next'

import { nodeStatusColor, runStatusColor } from '../lib'
import type { NodeRunStatus, RunStatus } from '../types'

export function RunStatusChip({ status }: { status: RunStatus }) {
  const { t } = useTranslation()
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full border bg-muted/40 px-2 py-0.5 text-xs">
      <span
        className={`inline-block h-2 w-2 rounded-full ${runStatusColor[status] ?? 'bg-zinc-400'} ${status === 'running' || status === 'queued' ? 'animate-pulse' : ''}`}
      />
      {t(`workflow.statuses.${status}`)}
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
