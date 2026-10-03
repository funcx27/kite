// Workflow list (design §11): name/description column, latest-run column with
// status chip, row actions (run with param form / runs list / webhook token /
// edit / delete). The list API carries no trigger config, so trigger badges
// live in the editor only.
import { useMemo, useState } from 'react'
import { useSpaces } from '@/platform/space-context'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { ActionTable, type Action } from '@/components/action-table'

import {
  useDeleteWorkflowMutation,
  useLatestRuns,
  useWorkflow,
  useWorkflows,
} from '../api'
import { RunParamsDialog, WebhookTokenDialog } from '../components/run-dialogs'
import { RunStatusChip } from '../components/status-chip'
import type { RunSummary, TriggerRunResponse, WorkflowListItem } from '../types'

// Union of one deploy-target field across the given workflows, sorted.
function aggregateUnique(
  workflows: WorkflowListItem[],
  pick: (wf: WorkflowListItem) => string[] | undefined
): string[] {
  return [...new Set(workflows.flatMap((wf) => pick(wf) ?? []))].sort()
}

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
  const { currentSpace } = useSpaces()
  const { data: workflows, isLoading } = useWorkflows()
  const [search, setSearch] = useState('')
  // 'all' is the sentinel for "no filter" — Radix SelectItem rejects ''.
  const [appFilter, setAppFilter] = useState('all')
  const [clusterFilter, setClusterFilter] = useState('all')
  // Run-status filter matches the latest run; 'none' = no run yet.
  const [statusFilter, setStatusFilter] = useState('all')
  const [runTarget, setRunTarget] = useState<WorkflowListItem | null>(null)
  const [tokenTarget, setTokenTarget] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<WorkflowListItem | null>(
    null
  )
  const deleteMutation = useDeleteWorkflowMutation()

  const spaceFiltered = useMemo(
    () =>
      (workflows ?? []).filter(
        (wf) => currentSpace === null || wf.namespace === currentSpace
      ),
    [workflows, currentSpace]
  )
  // Filter options aggregate the current list's deploy targets (deduped and
  // sorted), so entries never appear that no visible workflow matches.
  const appOptions = useMemo(
    () => aggregateUnique(spaceFiltered, (wf) => wf.apps),
    [spaceFiltered]
  )
  const clusterOptions = useMemo(
    () => aggregateUnique(spaceFiltered, (wf) => wf.clusters),
    [spaceFiltered]
  )

  // Latest runs fetched once per visible workflow (parent-level map) so the
  // status filter and the 最近运行 column share the same requests.
  const runNames = useMemo(
    () => spaceFiltered.map((wf) => wf.name),
    [spaceFiltered]
  )
  const { latestByName, pendingNames } = useLatestRuns(runNames)

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return spaceFiltered.filter(
      (wf) =>
        (appFilter === 'all' || (wf.apps ?? []).includes(appFilter)) &&
        (clusterFilter === 'all' ||
          (wf.clusters ?? []).includes(clusterFilter)) &&
        (statusFilter === 'all' ||
          (statusFilter === 'none' && !latestByName[wf.name]) ||
          latestByName[wf.name]?.status === statusFilter) &&
        (q === '' ||
          wf.name.toLowerCase().includes(q) ||
          wf.description.toLowerCase().includes(q))
    )
  }, [
    spaceFiltered,
    search,
    appFilter,
    clusterFilter,
    statusFilter,
    latestByName,
  ])

  const onTriggered = (run: TriggerRunResponse) => {
    if (runTarget) {
      navigate(`/workflows/${runTarget.name}/runs/${run.id}`)
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('workflow.list.search')}
            className="w-56"
          />
          <Select value={clusterFilter} onValueChange={setClusterFilter}>
            <SelectTrigger className="w-40">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">
                {t('workflow.list.allClusters')}
              </SelectItem>
              {clusterOptions.map((cluster) => (
                <SelectItem key={cluster} value={cluster}>
                  {cluster}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={appFilter} onValueChange={setAppFilter}>
            <SelectTrigger className="w-40">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t('workflow.list.allApps')}</SelectItem>
              {appOptions.map((app) => (
                <SelectItem key={app} value={app}>
                  {app}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={statusFilter} onValueChange={setStatusFilter}>
            <SelectTrigger className="w-40">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">
                {t('workflow.list.allRunStatuses')}
              </SelectItem>
              <SelectItem value="succeeded">
                {t('workflow.statuses.succeeded')}
              </SelectItem>
              <SelectItem value="failed">
                {t('workflow.statuses.failed')}
              </SelectItem>
              <SelectItem value="running">
                {t('workflow.statuses.running')}
              </SelectItem>
              <SelectItem value="none">
                {t('workflow.list.runStatusNone')}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
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
            cell: ({ row }) => (
              <LatestRunCell
                name={row.original.name}
                run={latestByName[row.original.name]}
                loading={pendingNames.includes(row.original.name)}
              />
            ),
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
              label: t('workflow.list.copyWorkflow'),
              onClick: (wf) =>
                navigate(`/workflows/new?copy=${encodeURIComponent(wf.name)}`),
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

// Renders from the parent's latest-run map (useLatestRuns); no query of its own.
function LatestRunCell({
  name,
  run,
  loading,
}: {
  name: string
  run: RunSummary | undefined
  loading: boolean
}) {
  const { t } = useTranslation()

  if (loading) {
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
