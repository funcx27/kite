// Run list of one workflow (design §10: 倒序分页): run number, status,
// revision, trigger, operator, start time and duration; rows open the run
// view.
import { useTranslation } from 'react-i18next'
import { Link, useNavigate, useParams } from 'react-router-dom'

import { formatDate } from '@/lib/utils'
import { usePageTitle } from '@/hooks/use-page-title'
import { Badge } from '@/components/ui/badge'
import { ActionTable } from '@/components/action-table'

import { useRuns } from '../api'
import { RunStatusChip } from '../components/status-chip'
import { formatDuration } from '../lib'

export function WorkflowRunsPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { name = '' } = useParams()
  const { data, isLoading } = useRuns(name)
  usePageTitle(t('workflow.list.runsTitle', { name }))

  const runs = data?.runs ?? []

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">
            {t('workflow.list.runsTitle', { name })}
          </h1>
          <p className="text-sm text-muted-foreground">
            <Link to={`/workflows/${name}`} className="hover:underline">
              ← {name}
            </Link>
          </p>
        </div>
      </div>

      <ActionTable
        data={runs}
        columns={[
          {
            accessorKey: 'runNumber',
            header: t('workflow.list.runCol'),
            cell: ({ row }) => (
              <Link
                to={`/workflows/${name}/runs/${row.original.id}`}
                className="font-mono font-medium hover:underline"
              >
                #{row.original.runNumber}
              </Link>
            ),
          },
          {
            accessorKey: 'status',
            header: t('workflow.list.status'),
            cell: ({ row }) => <RunStatusChip status={row.original.status} />,
          },
          {
            accessorKey: 'revisionRev',
            header: t('workflow.list.revision'),
            cell: ({ row }) => (
              <Badge variant="outline" className="font-mono">
                rev {row.original.revisionRev}
              </Badge>
            ),
          },
          {
            accessorKey: 'triggerType',
            header: t('workflow.list.trigger'),
            cell: ({ row }) => (
              <span className="text-sm">
                {t(
                  `workflow.list.trigger${capitalize(row.original.triggerType)}`
                )}
              </span>
            ),
          },
          {
            accessorKey: 'triggeredByName',
            header: t('workflow.list.by'),
            cell: ({ row }) => (
              <span className="text-sm text-muted-foreground">
                {row.original.triggeredByName || '—'}
              </span>
            ),
          },
          {
            accessorKey: 'startedAt',
            header: t('workflow.list.startedAt'),
            cell: ({ row }) => (
              <span className="text-xs text-muted-foreground">
                {(row.original.startedAt &&
                  formatDate(row.original.startedAt)) ||
                  '—'}
              </span>
            ),
          },
          {
            id: 'duration',
            header: t('workflow.list.duration'),
            cell: ({ row }) => (
              <span className="font-mono text-xs text-muted-foreground">
                {formatDuration(
                  row.original.startedAt,
                  row.original.finishedAt
                )}
              </span>
            ),
          },
        ]}
        actions={[
          {
            label: t('workflow.list.openRun'),
            onClick: (run) => navigate(`/workflows/${name}/runs/${run.id}`),
          },
        ]}
      />

      {isLoading && (
        <div className="py-6 text-center text-sm text-muted-foreground">
          {t('common.messages.loading')}
        </div>
      )}
      {!isLoading && runs.length === 0 && (
        <div className="py-6 text-center text-sm text-muted-foreground">
          {t('workflow.list.runsEmpty')}
        </div>
      )}
    </div>
  )
}

// triggerManual / triggerWebhook / triggerCron i18n keys.
function capitalize(value: string): string {
  return value.charAt(0).toUpperCase() + value.slice(1)
}
