import { useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import * as yaml from 'js-yaml'
import { useTranslation } from 'react-i18next'
import { Link, useParams } from 'react-router-dom'
import { toast } from 'sonner'

import {
  createApplicationConfig,
  fetchApplicationConfig,
  useApplication,
} from '@/lib/api/applications'
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
import {
  ApplicationWizard,
  type ApplicationWizardMode,
} from '@/components/application/application-wizard'
import { ClusterTab } from '@/components/application/cluster-tab'
import { DeployDialog } from '@/components/application/deploy-dialog'
import { HistoryDialog } from '@/components/application/history-dialog'
import { MergeDialog } from '@/components/application/merge-dialog'
import { OfflineDialog } from '@/components/application/offline-dialog'
import { ApplicationStatusChip } from '@/components/application/status-badge'

type DialogName =
  'deploy' | 'merge' | 'offline' | 'history' | 'copy' | 'lastApplied' | 'wizard'

// Application detail page (design §11): cluster-tab layout. The deploy
// dialog is the only modify+publish entry; merge is reachable only from the
// conflict states; history rollback writes back as pending.
export function ApplicationDetailPage() {
  const { t } = useTranslation()
  const { name } = useParams<{ name: string }>()
  const queryClient = useQueryClient()
  const { clusters } = useCluster()
  const { data: app, isLoading, error } = useApplication(name)
  usePageTitle(app ? `${app.namespace} / ${app.name}` : t('nav.applications'))

  const [activeTab, setActiveTab] = useState<string>('')
  const [openDialog, setOpenDialog] = useState<DialogName | null>(null)
  const [wizardMode, setWizardMode] = useState<ApplicationWizardMode>('cluster')
  const [copyTarget, setCopyTarget] = useState('')
  const [lastAppliedYaml, setLastAppliedYaml] = useState('')
  // Sticky once the first-publish flow starts: creating the cluster config
  // flips the app to "deployed" mid-flow, which must not close the dialog.
  const [firstPublishStarted, setFirstPublishStarted] = useState(false)

  const startFirstPublish = () => {
    setFirstPublishStarted(true)
    setOpenDialog('deploy')
  }

  const configs = useMemo(() => app?.configs ?? [], [app])
  const current =
    configs.find((config) => config.cluster === activeTab) ?? configs[0]

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
                    <span
                      className={`mr-1.5 inline-block h-2 w-2 rounded-full ${
                        config.statusSummary === 'synced'
                          ? 'bg-green-500'
                          : config.statusSummary === 'error'
                            ? 'bg-red-500'
                            : config.statusSummary === 'undeployed'
                              ? 'bg-zinc-400'
                              : 'bg-amber-500'
                      }`}
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

      {current && (
        <>
          <DeployDialog
            app={app}
            cluster={current.cluster}
            open={openDialog === 'deploy' && !firstPublishStarted}
            onOpenChange={(nextOpen) => {
              if (!nextOpen) setOpenDialog(null)
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
            onRolledBack={() => setOpenDialog('deploy')}
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
        onGoPublish={() => setOpenDialog('deploy')}
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
