// Workflow list (design §11): name/description column, latest-run column with
// status chip, row actions (run with param form / runs list / webhook token /
// edit / delete). The list API carries no trigger config, so trigger badges
// live in the editor only.
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate } from 'react-router-dom'
import { toast } from 'sonner'

import { formatDate } from '@/lib/utils'
import { usePageTitle } from '@/hooks/use-page-title'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { ActionTable, type Action } from '@/components/action-table'

import {
  useDeleteWorkflowMutation,
  useLatestRun,
  useWorkflow,
  useWorkflows,
} from '../api'
import { RunParamsDialog, WebhookTokenDialog } from '../components/run-dialogs'
import { RunStatusChip } from '../components/status-chip'
import type { TriggerRunResponse, WorkflowListItem } from '../types'

export function WorkflowListPage() {
  const { t } = useTranslation()
  usePageTitle(t('workflow.title'))

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-2xl font-bold">{t('workflow.title')}</h1>
        <p className="text-sm text-muted-foreground">
          {t('workflow.description')}
        </p>
      </div>
      <WorkflowList />
    </div>
  )
}

function WorkflowList() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: workflows, isLoading } = useWorkflows()
  const [search, setSearch] = useState('')
  const [runTarget, setRunTarget] = useState<WorkflowListItem | null>(null)
  const [tokenTarget, setTokenTarget] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<WorkflowListItem | null>(
    null
  )
  const deleteMutation = useDeleteWorkflowMutation()

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return (workflows ?? []).filter(
      (wf) =>
        q === '' ||
        wf.name.toLowerCase().includes(q) ||
        wf.description.toLowerCase().includes(q)
    )
  }, [workflows, search])

  const onTriggered = (run: TriggerRunResponse) => {
    if (runTarget) {
      navigate(`/workflows/${runTarget.name}/runs/${run.id}`)
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t('workflow.list.search')}
          className="w-56"
        />
        <Button onClick={() => navigate('/workflows/new')}>
          ＋ {t('workflow.list.create')}
        </Button>
      </div>

      <ActionTable<WorkflowListItem>
        data={filtered}
        columns={[
          {
            accessorKey: 'name',
            header: t('workflow.list.name'),
            cell: ({ row }) => (
              <div className="min-w-0">
                <Link
                  to={`/workflows/${row.original.name}`}
                  className="font-mono font-medium hover:underline"
                >
                  {row.original.name}
                </Link>
                <div className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
                  <span className="inline-flex items-center rounded-full border bg-muted/40 px-1.5 py-0.5">
                    {row.original.namespace}
                  </span>
                  <span className="truncate">
                    {row.original.description || '—'}
                  </span>
                </div>
              </div>
            ),
          },
          {
            id: 'latestRun',
            header: t('workflow.list.latestRun'),
            cell: ({ row }) => <LatestRunCell name={row.original.name} />,
          },
          {
            accessorKey: 'currentRev',
            header: t('workflow.list.revision'),
            cell: ({ row }) => (
              <Badge variant="outline" className="font-mono">
                rev {row.original.currentRev}
              </Badge>
            ),
          },
          {
            accessorKey: 'updatedAt',
            header: t('workflow.list.updatedAt'),
            cell: ({ row }) => (
              <span className="text-xs text-muted-foreground">
                {formatDate(row.original.updatedAt)}
              </span>
            ),
          },
        ]}
        actions={
          [
            {
              label: t('workflow.list.run'),
              onClick: (wf) => setRunTarget(wf),
            },
            {
              label: t('workflow.list.viewRuns'),
              onClick: (wf) => navigate(`/workflows/${wf.name}/runs`),
            },
            {
              label: t('workflow.list.webhookToken'),
              onClick: (wf) => setTokenTarget(wf.name),
            },
            {
              label: t('workflow.list.edit'),
              onClick: (wf) => navigate(`/workflows/${wf.name}`),
            },
            {
              label: t('common.actions.delete'),
              onClick: (wf) => setDeleteTarget(wf),
            },
          ] as Action<WorkflowListItem>[]
        }
      />

      {isLoading && (
        <div className="py-6 text-center text-sm text-muted-foreground">
          {t('common.messages.loading')}
        </div>
      )}
      {!isLoading && filtered.length === 0 && (
        <div className="py-6 text-center text-sm text-muted-foreground">
          {t('workflow.list.empty')}
        </div>
      )}

      {runTarget && (
        <RunParamsDialogHost
          name={runTarget.name}
          onOpenChange={() => setRunTarget(null)}
          onTriggered={onTriggered}
        />
      )}
      <WebhookTokenDialog
        name={tokenTarget ?? ''}
        open={tokenTarget !== null}
        onOpenChange={(open) => !open && setTokenTarget(null)}
      />

      <Dialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
      >
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle className="text-destructive">
              {t('workflow.list.deleteTitle', {
                name: deleteTarget?.name ?? '',
              })}
            </DialogTitle>
            <DialogDescription>
              {t('workflow.list.deleteDescription')}
            </DialogDescription>
          </DialogHeader>
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={() => setDeleteTarget(null)}>
              {t('common.actions.cancel')}
            </Button>
            <Button
              variant="destructive"
              disabled={deleteMutation.isPending}
              onClick={() => {
                if (!deleteTarget) return
                deleteMutation.mutate(deleteTarget.name, {
                  onSuccess: () => {
                    toast.success(t('workflow.list.deleted'))
                    setDeleteTarget(null)
                  },
                  onError: (err) => toast.error(err.message),
                })
              }}
            >
              {t('common.actions.delete')}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}

// Manual trigger needs the params schema, which the list API omits; the
// dialog host loads the detail for the picked workflow.
function RunParamsDialogHost({
  name,
  onOpenChange,
  onTriggered,
}: {
  name: string
  onOpenChange: (open: boolean) => void
  onTriggered: (run: TriggerRunResponse) => void
}) {
  const { data: workflow } = useWorkflow(name)
  return (
    <RunParamsDialog
      name={name}
      params={workflow?.params ?? []}
      open={workflow !== undefined}
      onOpenChange={onOpenChange}
      onTriggered={onTriggered}
    />
  )
}

function LatestRunCell({ name }: { name: string }) {
  const { t } = useTranslation()
  const { data: run, isLoading } = useLatestRun(name)

  if (isLoading) {
    return <span className="text-xs text-muted-foreground">…</span>
  }
  if (!run) {
    return (
      <span className="text-xs text-muted-foreground">
        {t('workflow.list.noRuns')}
      </span>
    )
  }
  return (
    <div className="flex flex-col gap-0.5">
      <Link
        to={`/workflows/${name}/runs/${run.id}`}
        className="inline-flex items-center gap-1.5 text-sm hover:underline"
      >
        <RunStatusChip status={run.status} />
        <span className="font-mono text-xs text-muted-foreground">
          #{run.runNumber}
        </span>
      </Link>
      <span className="text-xs text-muted-foreground">
        {(run.startedAt && formatDate(run.startedAt)) || '—'}
        {run.triggeredByName ? ` · ${run.triggeredByName}` : ''}
      </span>
    </div>
  )
}
