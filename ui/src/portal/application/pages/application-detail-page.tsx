import { useEffect, useMemo, useState } from 'react'
import { useApplication, useApplicationConfig } from '@/portal/application/api'
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
import { useWorkflows } from '@/portal/workflow/api'
import * as yaml from 'js-yaml'
import { Pencil, Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate, useParams } from 'react-router-dom'

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

      <RelatedWorkflows
        app={app}
        cluster={current?.cluster ?? currentCluster}
      />

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
// composites). Deliberately light — the name links into the runs list (the
// primary question is "how is this app's pipeline doing"), the editor is an
// icon action, no run data fetched.
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

  const createWorkflow = () => {
    const params = new URLSearchParams({
      fromApp: app.name,
      fromSpace: app.namespace,
    })
    if (cluster) params.set('cluster', cluster)
    navigate(`/workflows/new?${params.toString()}`)
  }

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
            <li
              key={wf.id}
              className="flex items-center justify-between gap-3 py-2"
            >
              <div className="min-w-0">
                <Link
                  to={`/workflows/${wf.id}/runs`}
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
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7"
                aria-label={t('common.actions.edit')}
                title={t('common.actions.edit')}
                onClick={() => navigate(`/workflows/${wf.id}`)}
              >
                <Pencil className="h-3.5 w-3.5" />
              </Button>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
