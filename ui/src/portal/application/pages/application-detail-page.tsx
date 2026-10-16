import { Fragment, useEffect, useMemo, useState } from 'react'
import {
  fetchApplicationConfig,
  useApplication,
  useApplicationConfig,
} from '@/portal/application/api'
import {
  ApplicationWizard,
  type ApplicationWizardMode,
} from '@/portal/application/components/application-wizard'
import { ClusterTab } from '@/portal/application/components/cluster-tab'
import { DeployDialog } from '@/portal/application/components/deploy-dialog'
import { HistoryDialog } from '@/portal/application/components/history-dialog'
import { OfflineDialog } from '@/portal/application/components/offline-dialog'
import { ApplicationStatusChip } from '@/portal/application/components/status-badge'
import { YamlDiffViewer } from '@/portal/application/components/yaml-diff-viewer'
import type {
  Application,
  ApplicationConfigSummary,
  ApplicationPendingChange,
} from '@/portal/application/types'
import { downloadTextFile } from '@/portal/components/download'
import {
  useInvalidateWorkflow,
  useRun,
  useRunActionMutation,
  useRuns,
  useWorkflows,
} from '@/portal/workflow/api'
import { RunParamsDialogHost } from '@/portal/workflow/components/run-dialogs'
import { RunStatusChip } from '@/portal/workflow/components/status-chip'
import type {
  DAGSpec,
  NodeRunStatus,
  WorkflowListItem,
  WorkflowNode,
} from '@/portal/workflow/types'
import * as yaml from 'js-yaml'
import {
  AlertTriangle,
  Check,
  ChevronRight,
  CircleDashed,
  Download,
  Loader2,
  Pencil,
  Play,
  Plus,
  RotateCcw,
  X,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Link, useLocation, useNavigate, useParams } from 'react-router-dom'
import { toast } from 'sonner'

import { formatDate, translateError } from '@/lib/utils'
import { useCluster } from '@/hooks/use-cluster'
import { usePageTitle } from '@/hooks/use-page-title'
import { Button } from '@/components/ui/button'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

type DialogName = 'deploy' | 'deploy-copy' | 'offline' | 'history' | 'wizard'

// Application detail page (design §11): cluster-tab layout. The deploy
// dialog is the only modify+publish entry — existing configs, the first
// publish of an undeployed app, and copy-to-another-cluster (createCopyFrom)
// all go through it; drift side-taking lives in the cluster tab; history
// rollback persists and publishes in one confirm.
export function ApplicationDetailPage() {
  const { t } = useTranslation()
  const {
    space,
    name,
    cluster: clusterParam,
  } = useParams<{
    space: string
    name: string
    cluster: string
  }>()
  const navigate = useNavigate()
  const location = useLocation()
  const { currentCluster } = useCluster()
  const { data: app, isLoading, error } = useApplication(space, name)
  usePageTitle(app ? `${app.namespace} / ${app.name}` : t('nav.applications'))

  const [openDialog, setOpenDialog] = useState<DialogName | null>(null)
  const [wizardMode, setWizardMode] = useState<ApplicationWizardMode>('cluster')
  // Copy-create flow: the cluster whose config is deep-copied (the tab the
  // menu was opened from); null unless the copy deploy dialog is open.
  const [copySource, setCopySource] = useState<string | null>(null)
  // Wizard edits waiting for [确认发布]: carried into the deploy dialog and
  // only persisted together with the publish; cancelling discards them.
  const [pendingChange, setPendingChange] =
    useState<ApplicationPendingChange | null>(null)

  const configs = useMemo(() => app?.configs ?? [], [app])
  // The active cluster is a URL path segment (app → cluster → pod hierarchy),
  // so views are shareable; a missing or stale segment falls back to the
  // first config.
  const current =
    configs.find((config) => config.cluster === clusterParam) ?? configs[0]

  useEffect(() => {
    if (configs.length === 0) return
    if (clusterParam && configs.some((c) => c.cluster === clusterParam)) return
    navigate(`/applications/${space}/${name}/clusters/${configs[0].cluster}`, {
      replace: true,
    })
  }, [configs, clusterParam, space, name, navigate])

  // setActiveTab moves between cluster routes; an empty value drops back to
  // the bare app route and lets the effect above pick the first config.
  const setActiveTab = (cluster: string) =>
    cluster
      ? navigate(`/applications/${space}/${name}/clusters/${cluster}`)
      : navigate(`/applications/${space}/${name}`, { replace: true })

  const undeployed = configs.length === 0

  const [exporting, setExporting] = useState(false)
  // The stored manifests of the active cluster tab as a multi-document YAML
  // file. The API view is already server-side redacted, so "***" secret
  // markers are what lands in the file.
  const exportConfig = async () => {
    if (!app || !current) return
    setExporting(true)
    try {
      const config = await fetchApplicationConfig(
        app.namespace,
        app.name,
        current.cluster
      )
      downloadTextFile(
        `${app.namespace}-${app.name}-${current.cluster}.yaml`,
        // js-yaml has no dumpAll; join per-document dumps with the
        // multi-document separator (same shape the backend writes).
        config.manifests.map((doc) => yaml.dump(doc)).join('---\n'),
        'text/yaml'
      )
    } catch (err) {
      toast.error(translateError(err, t))
    } finally {
      setExporting(false)
    }
  }

  const openWizardForEdit = () => {
    setWizardMode('cluster')
    setOpenDialog('wizard')
  }

  if (isLoading) {
    return (
      <div className="py-10 text-center text-sm text-muted-foreground">
        {t('common.messages.loading')}
      </div>
    )
  }

  if (error || !app) {
    return (
      <div className="py-10 text-center text-sm text-destructive">
        {t('applications.detail.notFound', { name: name ?? '' })}
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <div>
        <Link
          to="/applications"
          className="text-sm text-muted-foreground hover:text-foreground"
        >
          ← {t('nav.applications')}
        </Link>
        <div className="mt-0.5 flex flex-wrap items-baseline gap-3">
          <h1 className="font-mono text-base font-semibold">
            {app.namespace} / {app.name}
          </h1>
          <span className="text-sm text-muted-foreground">
            {app.description || '—'}
          </span>
          <div className="ml-auto flex items-center gap-2 self-center">
            <span className="text-xs text-muted-foreground/70">
              {t('applications.detail.exportMaskedHint')}
            </span>
            <Button
              size="sm"
              variant="outline"
              disabled={undeployed || !current || exporting}
              title={t('applications.detail.exportMaskedHint')}
              onClick={() => void exportConfig()}
            >
              <Download className="h-3.5 w-3.5" />
              {t('applications.detail.export')}
            </Button>
          </div>
        </div>
      </div>

      {/* The app's pipeline hub sits under the cluster tabs (above the tab
          content); the undeployed empty state keeps it after the basic info.
          returnTo pins the current app route so the workflow pages can bounce
          the user back here. */}
      {undeployed ? (
        <>
          <RelatedWorkflows
            app={app}
            cluster={current?.cluster ?? currentCluster}
            returnTo={`${location.pathname}${location.search}`}
          />
          <div className="flex flex-col items-center gap-2 rounded-xl border bg-card px-6 py-16 text-center">
            <ApplicationStatusChip status="undeployed" />
            <div className="text-sm text-muted-foreground">
              {t('applications.detail.undeployedTitle')}
            </div>
            <div className="text-xs text-muted-foreground/70">
              {t('applications.detail.undeployedHint')}
            </div>
            <Button
              size="sm"
              className="mt-2"
              onClick={() => {
                setCopySource(null)
                setOpenDialog('deploy')
              }}
            >
              {t('applications.detail.firstPublish')}
            </Button>
          </div>
        </>
      ) : (
        <>
          {configs.length > 0 && (
            <Tabs value={current?.cluster} onValueChange={setActiveTab}>
              <TabsList>
                {configs.map((config) => (
                  <TabsTrigger key={config.cluster} value={config.cluster}>
                    {config.cluster}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
          )}
          <RelatedWorkflows
            app={app}
            cluster={current?.cluster ?? currentCluster}
            returnTo={`${location.pathname}${location.search}`}
          />
          {current &&
            (current.spaceUnlinked ? (
              <UnlinkedClusterPanel
                app={app}
                config={current}
                onCopyConfig={() => {
                  setCopySource(current.cluster)
                  setOpenDialog('deploy-copy')
                }}
              />
            ) : (
              <ClusterTab
                key={current.cluster}
                app={app}
                config={current}
                active={true}
                onDeploy={() => {
                  setCopySource(null)
                  setOpenDialog('deploy')
                }}
                onOffline={() => setOpenDialog('offline')}
                onHistory={() => setOpenDialog('history')}
                onCopyConfig={() => {
                  setCopySource(current.cluster)
                  setOpenDialog('deploy-copy')
                }}
              />
            ))}
        </>
      )}

      {current && (
        <>
          <DeployDialog
            app={app}
            cluster={current.cluster}
            open={openDialog === 'deploy'}
            pending={pendingChange ?? undefined}
            onOpenChange={(nextOpen) => {
              if (!nextOpen) {
                setOpenDialog(null)
                setPendingChange(null)
              }
            }}
            onEditConfig={openWizardForEdit}
          />
          <OfflineDialog
            app={app}
            cluster={current.cluster}
            open={openDialog === 'offline'}
            onOpenChange={(nextOpen) => {
              if (!nextOpen) setOpenDialog(null)
            }}
            onOfflined={(result) => {
              setCopySource(null)
              if (result.configDeleted) {
                setActiveTab('')
              }
            }}
          />
          <HistoryDialog
            app={app}
            cluster={current.cluster}
            open={openDialog === 'history'}
            onOpenChange={(nextOpen) => {
              if (!nextOpen) setOpenDialog(null)
            }}
          />
        </>
      )}

      {/* First publish of a zero-config app (recovery: failed first publish
          or everything offlined): stays open until the publish succeeds —
          `undeployed` only flips once the confirm request has landed. */}
      <DeployDialog
        app={app}
        firstPublish={true}
        open={undeployed && openDialog === 'deploy'}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) setOpenDialog(null)
        }}
        onEditConfig={() => {
          setCopySource(null)
          setWizardMode('draft')
          setOpenDialog('wizard')
        }}
      />

      {/* Copy-create: publish to an unbound cluster, deep-copying the source
          tab's config as the base (menu entry 复制配置到其他集群). copySource
          survives the [修改配置] wizard round-trip and is cleared by every
          other deploy entry point. */}
      <DeployDialog
        app={app}
        createCopyFrom={copySource ?? undefined}
        open={openDialog === 'deploy-copy'}
        pending={pendingChange ?? undefined}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) {
            setOpenDialog(null)
            setPendingChange(null)
          }
        }}
        onEditConfig={openWizardForEdit}
      />

      <ApplicationWizard
        open={openDialog === 'wizard'}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) setOpenDialog(null)
        }}
        mode={wizardMode}
        app={app}
        cluster={wizardMode === 'cluster' ? current?.cluster : undefined}
        onGoPublish={(pending) => {
          setPendingChange(pending)
          // A wizard opened from the copy dialog edits the source cluster's
          // config; its edits go back to the copy flow's confirm request.
          setOpenDialog(copySource ? 'deploy-copy' : 'deploy')
        }}
      />
    </div>
  )
}

// Unlinked config: the namespace left the space, so the tab shows no runtime
// view at all — only the stored config stays reachable: view its YAML, or
// copy it to another cluster (the copy deploy dialog deep-copies this base).
function UnlinkedClusterPanel({
  app,
  config,
  onCopyConfig,
}: {
  app: Application
  config: ApplicationConfigSummary
  onCopyConfig: () => void
}) {
  const { t } = useTranslation()
  const [viewYaml, setViewYaml] = useState(false)
  const { data: detail } = useApplicationConfig(
    app.namespace,
    app.name,
    config.cluster
  )

  return (
    <div className="flex flex-col items-center gap-2 rounded-xl border bg-card px-6 py-16 text-center">
      <div className="text-sm font-medium">
        {t('applications.detail.unlinkedTitle')}
      </div>
      <div className="max-w-lg text-xs text-muted-foreground">
        {t('applications.detail.unlinkedHint')}
      </div>
      <div className="mt-2 flex items-center gap-2">
        <Button
          size="sm"
          variant="outline"
          disabled={!detail}
          onClick={() => setViewYaml(true)}
        >
          {t('applications.detail.viewConfigYaml')}
        </Button>
        <Button size="sm" onClick={onCopyConfig}>
          {t('applications.detail.copyToCluster')}
        </Button>
      </div>
      <YamlDiffViewer
        viewOnly
        raw
        open={viewYaml}
        onOpenChange={setViewYaml}
        content={viewYaml && detail ? yaml.dump(detail.manifests) : ''}
        title={t('applications.detail.configYamlTitle', {
          cluster: config.cluster,
        })}
      />
    </div>
  )
}

// Related workflows: workflows whose deploy-app nodes target this app
// (aggregated server-side into WorkflowListItem.apps as "space/name"
// composites) — the app page's pipeline hub: trigger a run inline, jump to
// the runs list or the editor, and see the latest run at each row's tail.
// Cross-page links carry fromApp/fromSpace/cluster/returnTo so the target
// page can send the user back to this app.
function RelatedWorkflows({
  app,
  cluster,
  returnTo,
}: {
  app: Application
  cluster?: string | null
  returnTo: string
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: workflows, isLoading } = useWorkflows()
  const [runTarget, setRunTarget] = useState<WorkflowListItem | null>(null)
  const related = useMemo(
    () =>
      (workflows ?? []).filter(
        (wf) =>
          (wf.apps ?? []).includes(`${app.namespace}/${app.name}`) &&
          // A workflow pinned to clusters only shows on its cluster tab;
          // unpinned ones show on every tab. With no active cluster, only
          // unpinned workflows qualify.
          ((wf.clusters ?? []).length === 0 ||
            (cluster != null && (wf.clusters ?? []).includes(cluster)))
      ),
    [workflows, app.namespace, app.name, cluster]
  )

  // Shared query params for every workflow-page hop from this app.
  const workflowParams = () => {
    const params = new URLSearchParams({
      fromApp: app.name,
      fromSpace: app.namespace,
      returnTo,
    })
    if (cluster) params.set('cluster', cluster)
    return params
  }
  const paramsQuery = workflowParams().toString()

  const createWorkflow = () => navigate(`/workflows/new?${paramsQuery}`)

  const editWorkflow = (wf: WorkflowListItem) =>
    navigate(`/workflows/${wf.id}?${paramsQuery}`)

  return (
    <section className="rounded-xl border bg-card">
      <div className="flex flex-wrap items-center gap-2 border-b px-4 py-3">
        <h2 className="text-sm font-semibold">
          {t('applications.detail.relatedWorkflows')}
        </h2>
        <Button
          variant="ghost"
          size="icon"
          className="h-6 w-6"
          aria-label={t('applications.detail.createWorkflow')}
          title={t('applications.detail.createWorkflow')}
          onClick={createWorkflow}
        >
          <Plus className="h-4 w-4" />
        </Button>
      </div>
      {isLoading ? (
        <p className="px-4 py-3 text-sm text-muted-foreground">
          {t('common.messages.loading')}
        </p>
      ) : related.length === 0 ? (
        <p className="px-4 py-3 text-sm text-muted-foreground">
          {t('applications.detail.relatedWorkflowsEmpty')}
        </p>
      ) : (
        <ul className="divide-y px-4 py-1">
          {related.map((wf) => (
            <RelatedWorkflowRow
              key={wf.id}
              wf={wf}
              paramsQuery={paramsQuery}
              onRun={() => setRunTarget(wf)}
              onEdit={() => editWorkflow(wf)}
            />
          ))}
        </ul>
      )}
      {runTarget && (
        <RunParamsDialogHost
          id={runTarget.id}
          name={runTarget.name}
          onOpenChange={(open) => {
            if (!open) setRunTarget(null)
          }}
          onTriggered={() => setRunTarget(null)}
        />
      )}
    </section>
  )
}

// One related-workflow row: the name links into the runs list with the same
// fromApp/fromSpace/cluster/returnTo params as the edit link; the tail shows
// the latest run's status chip and finish time. A latest run that has not
// reached a succeeded terminal state expands the node step bar under the
// name (only those rows fetch the run detail); succeeded rows keep the chip.
function RelatedWorkflowRow({
  wf,
  paramsQuery,
  onRun,
  onEdit,
}: {
  wf: WorkflowListItem
  paramsQuery: string
  onRun: () => void
  onEdit: () => void
}) {
  const { t } = useTranslation()
  const { data } = useRuns(wf.id, 1)
  const latest = data?.runs[0]
  return (
    <li className="py-2">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <Link
            to={`/workflows/${wf.id}/runs?${paramsQuery}`}
            className="font-mono text-sm font-medium hover:underline"
          >
            {wf.name}
          </Link>
          {wf.description && (
            <span className="ml-2 truncate text-xs text-muted-foreground">
              {wf.description}
            </span>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {latest && (
            <span className="mr-1 flex items-center gap-1.5">
              <RunStatusChip
                status={latest.status}
                failedNodes={latest.failedNodes}
              />
              <span className="text-xs text-muted-foreground">
                {latest.finishedAt ? formatDate(latest.finishedAt) : '—'}
              </span>
            </span>
          )}
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7"
            aria-label={t('workflow.list.run')}
            title={t('workflow.list.run')}
            onClick={onRun}
          >
            <Play className="h-3.5 w-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7"
            aria-label={t('common.actions.edit')}
            title={t('common.actions.edit')}
            onClick={onEdit}
          >
            <Pencil className="h-3.5 w-3.5" />
          </Button>
        </div>
      </div>
      {latest && latest.status !== 'succeeded' && (
        <RunProgressPreview runId={latest.id} />
      )}
    </li>
  )
}

// Run progress preview for a non-succeeded latest run: the run graph's nodes
// in topological order, each as name + status icon. Retry posts the existing
// /runs/:id/retry mutation and lets its query invalidation refresh the bar
// (useInvalidateWorkflow also covers the runs list so the tail chip follows).
function RunProgressPreview({ runId }: { runId: number }) {
  const { t } = useTranslation()
  const invalidate = useInvalidateWorkflow()
  const { data: run } = useRun(runId)
  const { retry } = useRunActionMutation()
  if (!run) return null
  const statusByNode = new Map(run.nodes.map((n) => [n.nodeId, n.status]))
  // Same gate as the engine (modules/workflow/internal/engine/actions.go):
  // only failed/cancelled runs accept a retry.
  const retryable = run.status === 'failed' || run.status === 'cancelled'
  return (
    <div className="mt-1.5 flex flex-wrap items-center gap-x-1 gap-y-1">
      {topoOrder(run.graph).map((node, index) => (
        <Fragment key={node.id}>
          {index > 0 && (
            <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground/40" />
          )}
          <RunStepNode
            node={node}
            status={statusByNode.get(node.id)}
            retryable={retryable}
            retrying={retry.isPending}
            onRetry={() =>
              retry.mutate(run.id, {
                onSuccess: () => {
                  invalidate()
                  toast.success(t('workflow.run.retryConfirmed'))
                },
                onError: (err) => toast.error(err.message),
              })
            }
          />
        </Fragment>
      ))}
    </div>
  )
}

// One step: node name plus a status icon. A continue-semantics failure
// (notify nodes are forced continue) does not block the run, so it paints as
// a warning instead of an error — the node-level class behind the run-level
// "succeeded (warnings)" badge. The retry icon mirrors the run page's gate:
// disabled unless the engine would accept the retry.
function RunStepNode({
  node,
  status,
  retryable,
  retrying,
  onRetry,
}: {
  node: WorkflowNode
  status: NodeRunStatus | undefined
  retryable: boolean
  retrying: boolean
  onRetry: () => void
}) {
  const { t } = useTranslation()
  const warning =
    status === 'failed' &&
    (node.type === 'notify' || node.onFailure === 'continue')
  return (
    <span
      className="flex items-center gap-1 text-xs"
      title={status ? t(`workflow.statuses.${status}`) : undefined}
    >
      <StepStatusIcon status={status} warning={warning} />
      <span className="max-w-40 truncate">
        {warning
          ? t('applications.detail.runNodeWithWarnings', {
              name: node.name || node.id,
            })
          : node.name || node.id}
      </span>
      {status === 'failed' && !warning && (
        <Button
          variant="ghost"
          size="icon"
          className="h-5 w-5"
          aria-label={t('workflow.run.retry')}
          title={
            retryable
              ? t('workflow.run.retry')
              : t('workflow.run.retryDisabledHint')
          }
          disabled={!retryable || retrying}
          onClick={onRetry}
        >
          <RotateCcw className="h-3 w-3" />
        </Button>
      )}
    </span>
  )
}

// Status icon of one step; statuses not reached in a non-succeeded run
// (pending/skipped/cancelled/interrupted) render as the not-reached circle.
function StepStatusIcon({
  status,
  warning,
}: {
  status: NodeRunStatus | undefined
  warning: boolean
}) {
  if (status === 'succeeded')
    return <Check className="h-3.5 w-3.5 shrink-0 text-green-500" />
  if (status === 'running')
    return (
      <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-blue-500" />
    )
  if (status === 'failed') {
    return warning ? (
      <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-amber-500" />
    ) : (
      <X className="h-3.5 w-3.5 shrink-0 text-red-500" />
    )
  }
  return <CircleDashed className="h-3.5 w-3.5 shrink-0 text-zinc-400" />
}

// Topological order of the run graph (Kahn); ties keep definition order so
// the bar renders stably. The server validates acyclicity; any leftovers
// (defensive) append in definition order.
function topoOrder(graph: DAGSpec): WorkflowNode[] {
  const indegree = new Map<string, number>(graph.nodes.map((n) => [n.id, 0]))
  const successors: Record<string, string[]> = {}
  for (const edge of graph.edges) {
    if (!indegree.has(edge.from) || !indegree.has(edge.to)) continue
    indegree.set(edge.to, (indegree.get(edge.to) ?? 0) + 1)
    successors[edge.from] = [...(successors[edge.from] ?? []), edge.to]
  }
  const byId = new Map(graph.nodes.map((n) => [n.id, n]))
  const queue = graph.nodes
    .filter((n) => indegree.get(n.id) === 0)
    .map((n) => n.id)
  const ordered: WorkflowNode[] = []
  while (queue.length > 0) {
    const id = queue.shift() as string
    const node = byId.get(id)
    if (node) ordered.push(node)
    for (const successor of successors[id] ?? []) {
      const remaining = (indegree.get(successor) ?? 0) - 1
      indegree.set(successor, remaining)
      if (remaining === 0) queue.push(successor)
    }
  }
  if (ordered.length < graph.nodes.length) {
    const seen = new Set(ordered.map((n) => n.id))
    ordered.push(...graph.nodes.filter((n) => !seen.has(n.id)))
  }
  return ordered
}
