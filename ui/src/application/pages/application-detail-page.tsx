import { useEffect, useMemo, useState } from 'react'
import {
  createApplicationConfig,
  fetchApplicationConfig,
  useApplication,
} from '@/application/api'
import {
  ApplicationWizard,
  type ApplicationWizardMode,
} from '@/application/components/application-wizard'
import { ClusterTab } from '@/application/components/cluster-tab'
import { DeployDialog } from '@/application/components/deploy-dialog'
import { HistoryDialog } from '@/application/components/history-dialog'
import { MergeDialog } from '@/application/components/merge-dialog'
import { OfflineDialog } from '@/application/components/offline-dialog'
import {
  ApplicationStatusChip,
  ClusterStatusDot,
} from '@/application/components/status-badge'
import type { Application, ApplicationPendingChange } from '@/application/types'
import { useWorkflows } from '@/workflow/api'
import { useQueryClient } from '@tanstack/react-query'
import * as yaml from 'js-yaml'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { toast } from 'sonner'

import { translateError } from '@/lib/utils'
import { useCluster } from '@/hooks/use-cluster'
import { usePageTitle } from '@/hooks/use-page-title'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

type DialogName =
  'deploy' | 'merge' | 'offline' | 'history' | 'copy' | 'lastApplied' | 'wizard'

// Application detail page (design §11): cluster-tab layout. The deploy
// dialog is the only modify+publish entry; merge is reachable only from the
// conflict states; history rollback persists and publishes in one confirm.
export function ApplicationDetailPage() {
  const { t } = useTranslation()
  const { name, cluster: clusterParam } = useParams<{
    name: string
    cluster: string
  }>()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { clusters, currentCluster } = useCluster()
  const { data: app, isLoading, error } = useApplication(name)
  usePageTitle(app ? `${app.namespace} / ${app.name}` : t('nav.applications'))

  const [openDialog, setOpenDialog] = useState<DialogName | null>(null)
  const [wizardMode, setWizardMode] = useState<ApplicationWizardMode>('cluster')
  const [copyTarget, setCopyTarget] = useState('')
  const [lastAppliedYaml, setLastAppliedYaml] = useState('')
  // Wizard edits waiting for [确认发布]: carried into the deploy dialog and
  // only persisted together with the publish; cancelling discards them.
  const [pendingChange, setPendingChange] =
    useState<ApplicationPendingChange | null>(null)
  // Sticky once the first-publish flow starts: creating the cluster config
  // flips the app to "deployed" mid-flow, which must not close the dialog.
  const [firstPublishStarted, setFirstPublishStarted] = useState(false)

  const startFirstPublish = () => {
    setFirstPublishStarted(true)
    setOpenDialog('deploy')
  }

  const configs = useMemo(() => app?.configs ?? [], [app])
  // The active cluster is a URL path segment (app → cluster → pod hierarchy),
  // so views are shareable; a missing or stale segment falls back to the
  // first config.
  const current =
    configs.find((config) => config.cluster === clusterParam) ?? configs[0]

  useEffect(() => {
    if (configs.length === 0) return
    if (clusterParam && configs.some((c) => c.cluster === clusterParam)) return
    navigate(`/applications/${name}/clusters/${configs[0].cluster}`, {
      replace: true,
    })
  }, [configs, clusterParam, name, navigate])

  // setActiveTab moves between cluster routes; an empty value drops back to
  // the bare app route and lets the effect above pick the first config.
  const setActiveTab = (cluster: string) =>
    cluster
      ? navigate(`/applications/${name}/clusters/${cluster}`)
      : navigate(`/applications/${name}`, { replace: true })

  const undeployed = configs.length === 0
  const availableCopyTargets = useMemo(
    () =>
      clusters.filter(
        (candidate) =>
          !candidate.error &&
          candidate.name !== current?.cluster &&
          !configs.some((config) => config.cluster === candidate.name)
      ),
    [clusters, configs, current?.cluster]
  )

  const openWizardForEdit = () => {
    setWizardMode('cluster')
    setOpenDialog('wizard')
  }

  const viewLastApplied = async () => {
    if (!app || !current) return
    try {
      const detail = await fetchApplicationConfig(app.name, current.cluster)
      setLastAppliedYaml(yaml.dump(detail.lastApplied ?? []))
      setOpenDialog('lastApplied')
    } catch (err) {
      toast.error(translateError(err, t))
    }
  }

  const copyConfig = async () => {
    if (!app || !current || !copyTarget) return
    try {
      await createApplicationConfig(app.name, {
        cluster: copyTarget,
        copyFrom: current.cluster,
      })
      await queryClient.invalidateQueries({ queryKey: ['applications'] })
      toast.success(
        t('applications.detail.copySuccess', {
          from: current.cluster,
          to: copyTarget,
        })
      )
      setOpenDialog(null)
      setCopyTarget('')
    } catch (err) {
      toast.error(translateError(err, t))
    }
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
        </div>
      </div>

      {undeployed ? (
        <div className="flex flex-col items-center gap-2 rounded-xl border bg-card px-6 py-16 text-center">
          <ApplicationStatusChip status="undeployed" />
          <div className="text-sm text-muted-foreground">
            {t('applications.detail.undeployedTitle')}
          </div>
          <div className="text-xs text-muted-foreground/70">
            {t('applications.detail.undeployedHint')}
          </div>
          <Button size="sm" className="mt-2" onClick={startFirstPublish}>
            {t('applications.detail.firstPublish')}
          </Button>
        </div>
      ) : (
        <>
          {configs.length > 0 && (
            <Tabs value={current?.cluster} onValueChange={setActiveTab}>
              <TabsList>
                {configs.map((config) => (
                  <TabsTrigger key={config.cluster} value={config.cluster}>
                    <ClusterStatusDot
                      cluster={config.cluster}
                      statusSummary={config.statusSummary}
                      className="mr-1.5"
                    />
                    {config.cluster}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
          )}
          {current && (
            <ClusterTab
              key={current.cluster}
              app={app}
              config={current}
              active={true}
              onDeploy={() => setOpenDialog('deploy')}
              onOffline={() => setOpenDialog('offline')}
              onMerge={() => setOpenDialog('merge')}
              onHistory={() => setOpenDialog('history')}
              onCopyConfig={() => setOpenDialog('copy')}
              onViewLastApplied={() => void viewLastApplied()}
            />
          )}
        </>
      )}

      <RelatedWorkflows
        app={app}
        cluster={current?.cluster ?? currentCluster}
      />

      {current && (
        <>
          <DeployDialog
            app={app}
            cluster={current.cluster}
            open={openDialog === 'deploy' && !firstPublishStarted}
            pending={pendingChange ?? undefined}
            onOpenChange={(nextOpen) => {
              if (!nextOpen) {
                setOpenDialog(null)
                setPendingChange(null)
              }
            }}
            onEditConfig={openWizardForEdit}
            onResolveConflict={() => setOpenDialog('merge')}
          />
          <MergeDialog
            app={app}
            cluster={current.cluster}
            open={openDialog === 'merge'}
            onOpenChange={(nextOpen) => {
              if (!nextOpen) setOpenDialog(null)
            }}
            onMerged={() => setOpenDialog('deploy')}
          />
          <OfflineDialog
            app={app}
            cluster={current.cluster}
            open={openDialog === 'offline'}
            onOpenChange={(nextOpen) => {
              if (!nextOpen) setOpenDialog(null)
            }}
            onOfflined={(result) => {
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

      <DeployDialog
        app={app}
        firstPublish={true}
        open={firstPublishStarted && openDialog === 'deploy'}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) {
            setOpenDialog(null)
            setFirstPublishStarted(false)
          }
        }}
        onEditConfig={() => {
          setWizardMode('draft')
          setOpenDialog('wizard')
        }}
        onConfigCreated={() => {
          void queryClient.invalidateQueries({ queryKey: ['applications'] })
        }}
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
          setOpenDialog('deploy')
        }}
      />

      <Dialog
        open={openDialog === 'copy'}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) setOpenDialog(null)
        }}
      >
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>{t('applications.detail.copyToCluster')}</DialogTitle>
            <DialogDescription className="sr-only">
              {t('applications.detail.copyToCluster')}
            </DialogDescription>
          </DialogHeader>
          <Select value={copyTarget} onValueChange={setCopyTarget}>
            <SelectTrigger>
              <SelectValue placeholder={t('applications.deploy.pickCluster')} />
            </SelectTrigger>
            <SelectContent>
              {availableCopyTargets.map((candidate) => (
                <SelectItem key={candidate.name} value={candidate.name}>
                  {candidate.name} · {candidate.version ?? ''}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className="text-xs text-muted-foreground">
            {t('applications.detail.copyHint')}
          </p>
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              onClick={() => {
                setOpenDialog(null)
                setCopyTarget('')
              }}
            >
              {t('common.actions.cancel')}
            </Button>
            <Button disabled={!copyTarget} onClick={() => void copyConfig()}>
              {t('common.actions.confirm')}
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      <Dialog
        open={openDialog === 'lastApplied'}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) setOpenDialog(null)
        }}
      >
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>
              {t('applications.detail.viewLastApplied')}
            </DialogTitle>
            <DialogDescription className="sr-only">
              {t('applications.detail.viewLastApplied')}
            </DialogDescription>
          </DialogHeader>
          <pre className="max-h-[60vh] overflow-auto rounded-lg bg-zinc-950 p-4 font-mono text-xs leading-5 text-zinc-300">
            {lastAppliedYaml || '—'}
          </pre>
        </DialogContent>
      </Dialog>
    </div>
  )
}

// Related workflows: workflows whose deploy-app nodes target this app
// (aggregated server-side into WorkflowListItem.apps). Deliberately light —
// an editor link per workflow plus a runs-list link, no run data fetched.
function RelatedWorkflows({
  app,
  cluster,
}: {
  app: Application
  cluster?: string | null
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: workflows, isLoading } = useWorkflows()
  const related = useMemo(
    () => (workflows ?? []).filter((wf) => (wf.apps ?? []).includes(app.name)),
    [workflows, app.name]
  )

  const createWorkflow = () => {
    const params = new URLSearchParams({ fromApp: app.name })
    if (cluster) params.set('cluster', cluster)
    navigate(`/workflows/new?${params.toString()}`)
  }

  return (
    <section className="rounded-xl border bg-card">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3">
        <h2 className="text-sm font-semibold">
          {t('applications.detail.relatedWorkflows')}
        </h2>
        <Button size="sm" variant="outline" onClick={createWorkflow}>
          ＋ {t('applications.detail.createWorkflow')}
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
            <li
              key={wf.id}
              className="flex items-center justify-between gap-3 py-2"
            >
              <div className="min-w-0">
                <Link
                  to={`/workflows/${wf.name}`}
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
              <Link
                to={`/workflows/${wf.name}/runs`}
                className="shrink-0 text-xs text-muted-foreground hover:text-foreground hover:underline"
              >
                {t('applications.detail.workflowRuns')}
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
