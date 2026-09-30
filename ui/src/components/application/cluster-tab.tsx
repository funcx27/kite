import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import type {
  Application,
  ApplicationConfigSummary,
  ApplicationDocDiff,
} from '@/types/application'
import {
  restartApplicationConfig,
  restartApplicationPod,
  useApplicationConfig,
  useApplicationResources,
  useApplicationStatus,
} from '@/lib/api/applications'
import { formatBytes, formatDate, translateError } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { PodMonitoringDialog } from './pod-monitoring-dialog'
import { ApplicationStatusChip } from './status-badge'

interface ClusterTabProps {
  app: Application
  config: ApplicationConfigSummary
  active: boolean
  onDeploy: () => void
  onOffline: () => void
  onMerge: () => void
  onHistory: () => void
  onCopyConfig: () => void
  onViewLastApplied: () => void
}

function podAge(startTime: string | undefined): string {
  if (!startTime) return '—'
  const ms = Date.now() - new Date(startTime).getTime()
  const days = Math.floor(ms / 86400000)
  if (days > 0) return `${days}d`
  const hours = Math.floor(ms / 3600000)
  if (hours > 0) return `${hours}h`
  return `${Math.floor(ms / 60000)}m`
}

// formatSpecPair renders "requests / limits" for CPU and memory, e.g.
// "100m/500m · 128Mi/512Mi"; an all-empty pair reads as a dash.
function formatSpecPair(
  requests?: { cpu?: string; memory?: string },
  limits?: { cpu?: string; memory?: string }
): string {
  const cpu = `${requests?.cpu || '—'}/${limits?.cpu || '—'}`
  const mem = `${requests?.memory || '—'}/${limits?.memory || '—'}`
  const pair = `${cpu} · ${mem}`
  return pair === '—/— · —/—' ? '—' : pair
}

// DiffPathsCard lists one diff group's documents and changed paths — the
// debug view behind both the conflict and merged consistency states.
function DiffPathsCard({
  tone,
  title,
  docs,
  footer,
}: {
  tone: 'amber' | 'sky'
  title: string
  docs: ApplicationDocDiff[]
  footer?: string
}) {
  const { t } = useTranslation()
  const box =
    tone === 'amber'
      ? 'border-amber-200 bg-amber-50/40 dark:border-amber-800 dark:bg-amber-950/40'
      : 'border-sky-200 bg-sky-50/40 dark:border-sky-800 dark:bg-sky-950/40'
  const heading =
    tone === 'amber'
      ? 'text-amber-700 dark:text-amber-400'
      : 'text-sky-700 dark:text-sky-400'
  return (
    <div className={`rounded-lg border p-3 ${box}`}>
      <div className={`mb-2 text-xs font-semibold ${heading}`}>{title}</div>
      <div className="space-y-2">
        {docs.map((docDiff) => (
          <div key={`${docDiff.kind}/${docDiff.name}`}>
            <div className="font-mono text-xs text-muted-foreground">
              {docDiff.kind}/{docDiff.name}
            </div>
            {(docDiff.paths ?? []).map((path) => (
              <div key={path} className="font-mono text-xs">
                {path}
              </div>
            ))}
            {(!docDiff.paths || docDiff.paths.length === 0) && (
              <div className="font-mono text-xs">
                {t('applications.detail.docLevelChange')}
              </div>
            )}
          </div>
        ))}
      </div>
      {footer && (
        <div className="mt-2 text-xs text-muted-foreground">{footer}</div>
      )}
    </div>
  )
}

// One cluster tab of the detail page (design §11): overview bar → pod table →
// network card → consistency card → warning events.
export function ClusterTab({
  app,
  config,
  active,
  onDeploy,
  onOffline,
  onMerge,
  onHistory,
  onCopyConfig,
  onViewLastApplied,
}: ClusterTabProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { data: detail } = useApplicationConfig(
    active ? app.name : undefined,
    active ? config.cluster : undefined
  )
  const { data: statusReport, refetch: refetchStatus } = useApplicationStatus(
    app.name,
    config.cluster,
    { disable: !active }
  )
  const { data: runtime } = useApplicationResources(app.name, config.cluster, {
    disable: !active,
    refreshInterval: active ? 5000 : 0,
  })
  const [podDialog, setPodDialog] = useState<{
    open: boolean
    pod: string
    view?: 'terminal' | 'logs'
  }>({ open: false, pod: '' })
  const [restarting, setRestarting] = useState(false)
  // Two-step restart confirm: the target ('config' or a pod name) arms on
  // the first click and auto-disarms, guarding against accidental restarts.
  const [confirmTarget, setConfirmTarget] = useState<string | null>(null)
  const confirmTimer = useRef<number | undefined>(undefined)

  useEffect(() => () => window.clearTimeout(confirmTimer.current), [])

  const askRestart = (target: string, run: () => void) => {
    window.clearTimeout(confirmTimer.current)
    if (confirmTarget !== target) {
      setConfirmTarget(target)
      confirmTimer.current = window.setTimeout(
        () => setConfirmTarget(null),
        3000
      )
      return
    }
    setConfirmTarget(null)
    run()
  }

  const namespace = config.namespace || app.namespace
  const status = statusReport?.status ?? config.statusSummary
  const drift = statusReport?.drift ?? []
  const pending = statusReport?.pending ?? []
  // Conflict means the publish gate is blocked (action=merge): drift alone
  // stays non-empty in the merged state until the next publish re-anchors.
  const isConflict = statusReport?.action === 'merge'
  const isOfflined =
    (config.statusSummary === 'undeployed' || status === 'undeployed') &&
    drift.length === 0

  const workload = runtime?.workload
  const workloadUnauthorized = !workload || workload.unauthorized

  const handleRestart = async () => {
    setRestarting(true)
    try {
      await restartApplicationConfig(app.name, config.cluster)
      toast.success(
        t('applications.detail.restartStarted', { cluster: config.cluster })
      )
      await queryClient.invalidateQueries({
        queryKey: ['applications', app.name, 'resources', config.cluster],
      })
    } catch (err) {
      toast.error(translateError(err, t))
    } finally {
      setRestarting(false)
    }
  }

  const handleRestartPod = async (podName: string) => {
    setRestarting(true)
    try {
      await restartApplicationPod(app.name, config.cluster, podName)
      toast.success(t('applications.detail.podRestarted', { pod: podName }))
      await queryClient.invalidateQueries({
        queryKey: ['applications', app.name, 'resources', config.cluster],
      })
    } catch (err) {
      toast.error(translateError(err, t))
    } finally {
      setRestarting(false)
    }
  }

  return (
    <div className="space-y-4">
      {/* Overview bar */}
      <div className="flex flex-wrap items-center gap-x-6 gap-y-2 rounded-xl border bg-card px-5 py-3.5">
        <span className="inline-flex items-center gap-1.5 rounded-full border border-zinc-200 bg-zinc-50 px-2.5 py-0.5 text-xs font-medium text-zinc-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-400">
          <span className="h-2 w-2 rounded-full bg-sky-500" />
          {workloadUnauthorized
            ? t('applications.detail.readyUnknown')
            : t('applications.detail.ready', {
                ready: workload?.ready ?? 0,
                replicas: workload?.replicas ?? 0,
              })}
        </span>
        <ApplicationStatusChip status={status} />
        <span className="text-xs text-muted-foreground">
          {(detail?.lastDeployedAt ?? config.lastDeployedAt)
            ? t('applications.detail.lastDeployed', {
                time: formatDate(
                  detail?.lastDeployedAt ?? config.lastDeployedAt ?? ''
                ),
              })
            : t('applications.detail.neverDeployed')}
          {detail?.lastSyncedAt
            ? ` · ${t('applications.detail.lastSynced', {
                time: formatDate(detail.lastSyncedAt),
              })}`
            : ''}
        </span>
        {runtime?.workload?.kind && (
          <span
            className="font-mono text-xs text-muted-foreground"
            title={t('applications.detail.controllerSpecHint')}
          >
            {t('applications.detail.controllerSpec')}
            {': '}
            {`${runtime.workload.limits?.cpu || '—'} · ${runtime.workload.limits?.memory || '—'}`}
          </span>
        )}
        {(runtime?.workload?.images?.length ?? 0) > 0 && (
          <span
            className="font-mono text-xs text-muted-foreground"
            title={t('applications.detail.controllerImageHint')}
          >
            {t('applications.detail.controllerImage')}
            {': '}
            {runtime!.workload.images!.join(' ')}
          </span>
        )}
        <div className="ml-auto flex items-center gap-2">
          <Button size="sm" onClick={onDeploy}>
            {t('applications.detail.publish')}
          </Button>
          <Button
            size="sm"
            variant="outline"
            className={
              confirmTarget === 'config'
                ? 'border-destructive text-destructive hover:bg-destructive/10 hover:text-destructive'
                : undefined
            }
            disabled={restarting || isOfflined}
            title={t('applications.detail.restartConfirmHint')}
            onClick={() => askRestart('config', () => void handleRestart())}
          >
            {confirmTarget === 'config'
              ? t('applications.detail.confirmRestart')
              : t('applications.detail.restart')}
          </Button>
          <Button size="sm" variant="outline" onClick={onOffline}>
            {t('applications.detail.offline')}
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button size="sm" variant="ghost" className="px-2">
                •••
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={onHistory}>
                {t('applications.detail.history')}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={onCopyConfig}>
                {t('applications.detail.copyToCluster')}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={onViewLastApplied}>
                {t('applications.detail.viewLastApplied')}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>

      {isOfflined ? (
        <div className="flex flex-col items-center gap-2 rounded-xl border bg-card px-6 py-16 text-center">
          <ApplicationStatusChip status="undeployed" />
          <div className="text-sm text-muted-foreground">
            {t('applications.detail.offlinedTitle')}
          </div>
          <div className="text-xs text-muted-foreground/70">
            {t('applications.detail.offlinedHint')}
          </div>
          <Button size="sm" className="mt-2" onClick={onDeploy}>
            {t('applications.detail.republish')}
          </Button>
        </div>
      ) : (
        <>
          {/* Pods */}
          <div className="overflow-hidden rounded-xl border bg-card">
            <div className="flex items-center justify-between border-b px-5 py-3">
              <span className="text-sm font-semibold">
                {t('applications.detail.pods', {
                  count: runtime?.pods?.items.length ?? 0,
                })}
              </span>
              {runtime?.pods?.metricsAvailable && (
                <span className="text-xs text-muted-foreground">
                  {t('applications.detail.autoRefresh')}
                </span>
              )}
            </div>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('applications.detail.podName')}</TableHead>
                  <TableHead>{t('applications.detail.podStatus')}</TableHead>
                  <TableHead>{t('applications.detail.podIP')}</TableHead>
                  <TableHead>{t('applications.detail.podUsage')}</TableHead>
                  <TableHead>{t('applications.detail.podRestarts')}</TableHead>
                  <TableHead>{t('applications.detail.podAge')}</TableHead>
                  <TableHead>{t('applications.detail.podNode')}</TableHead>
                  <TableHead className="text-right">
                    {t('common.fields.actions')}
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(runtime?.pods?.items ?? []).map((pod) => (
                  <TableRow key={pod.name}>
                    <TableCell className="font-mono text-xs">
                      <button
                        type="button"
                        className="hover:underline"
                        title={(pod.images ?? []).join('\n')}
                        onClick={() =>
                          setPodDialog({ open: true, pod: pod.name })
                        }
                      >
                        {pod.name}
                      </button>
                    </TableCell>
                    <TableCell>
                      <span
                        className={
                          (pod.status || pod.phase) === 'Running'
                            ? 'inline-flex items-center gap-1.5 rounded-full border border-green-200 bg-green-50 px-2 py-0.5 text-xs font-medium text-green-700 dark:border-green-800 dark:bg-green-950 dark:text-green-400'
                            : 'inline-flex items-center gap-1.5 rounded-full border border-amber-200 bg-amber-50 px-2 py-0.5 text-xs font-medium text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-400'
                        }
                      >
                        {pod.status || pod.phase || '—'}
                      </span>
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {pod.ip || '—'}
                    </TableCell>
                    <TableCell
                      className="font-mono text-xs"
                      title={t('applications.detail.podUsageHint')}
                    >
                      <div>
                        {pod.cpu !== undefined || pod.memory !== undefined
                          ? `${pod.cpu ?? 0}m / ${formatBytes(pod.memory ?? 0)}`
                          : '—'}
                      </div>
                      <div className="text-muted-foreground">
                        {formatSpecPair(pod.requests, pod.limits)}
                      </div>
                    </TableCell>
                    <TableCell>{pod.restarts}</TableCell>
                    <TableCell>{podAge(pod.startTime)}</TableCell>
                    <TableCell className="font-mono text-xs">
                      {pod.node || '—'}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="inline-flex gap-1">
                        <Button
                          size="sm"
                          variant="outline"
                          className={
                            confirmTarget === pod.name
                              ? 'border-destructive text-destructive hover:bg-destructive/10 hover:text-destructive'
                              : undefined
                          }
                          disabled={restarting}
                          title={t('applications.detail.restartConfirmHint')}
                          onClick={() =>
                            askRestart(
                              pod.name,
                              () => void handleRestartPod(pod.name)
                            )
                          }
                        >
                          {confirmTarget === pod.name
                            ? t('applications.detail.confirmRestart')
                            : t('applications.detail.restartShort')}
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
                {(runtime?.pods?.items.length ?? 0) === 0 && (
                  <TableRow>
                    <TableCell
                      colSpan={8}
                      className="py-8 text-center text-sm text-muted-foreground"
                    >
                      {runtime?.pods?.unauthorized
                        ? t('applications.detail.unauthorized')
                        : t('applications.detail.noPods')}
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>

          {/* Network */}
          <div className="rounded-xl border bg-card px-5 py-4">
            <div className="mb-3 text-sm font-semibold">
              {t('applications.detail.network')}
            </div>
            <div className="grid gap-3 md:grid-cols-2">
              {(runtime?.services?.items ?? []).map((service) => (
                <div key={service.name} className="rounded-lg border p-3">
                  <div className="flex items-center justify-between">
                    <span className="text-xs font-medium text-muted-foreground">
                      {t('applications.detail.service')} · {service.type || '—'}
                    </span>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-6 px-2 py-0 text-xs"
                      onClick={() => {
                        const text = `${service.name}.${namespace}:${(
                          service.ports ?? []
                        )
                          .map((port) => port.port)
                          .join(':')}`
                        void navigator.clipboard.writeText(text)
                        toast.success(t('common.messages.copied'))
                      }}
                    >
                      {t('common.actions.copy')}
                    </Button>
                  </div>
                  <div className="mt-1 font-mono text-sm">
                    {service.name} :{' '}
                    {(service.ports ?? []).map((port) => port.port).join(' · ')}
                  </div>
                  <div className="mt-0.5 font-mono text-xs text-muted-foreground">
                    {service.clusterIP || '—'}
                  </div>
                </div>
              ))}
              {(runtime?.ingresses?.items ?? []).map((ingress) => (
                <div key={ingress.name} className="rounded-lg border p-3">
                  <div className="flex items-center justify-between">
                    <span className="text-xs font-medium text-muted-foreground">
                      Ingress
                    </span>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-6 px-2 py-0 text-xs"
                      onClick={() => {
                        const host = ingress.rules?.[0]?.host
                        if (host) {
                          void navigator.clipboard.writeText(host)
                          toast.success(t('common.messages.copied'))
                        }
                      }}
                    >
                      {t('common.actions.copy')}
                    </Button>
                  </div>
                  <div className="mt-1 font-mono text-sm">
                    {ingress.rules
                      ?.map(
                        (rule) =>
                          `${rule.host || '*'} → ${(rule.paths ?? [])
                            .map((path) => path.path || '/')
                            .join(', ')}`
                      )
                      .join(' · ') || '—'}
                  </div>
                  <div className="mt-0.5 font-mono text-xs text-muted-foreground">
                    {(ingress.lbAddresses ?? []).join(', ') || '—'}
                  </div>
                </div>
              ))}
              {(runtime?.services?.items.length ?? 0) === 0 &&
                (runtime?.ingresses?.items.length ?? 0) === 0 && (
                  <div className="text-sm text-muted-foreground">
                    {runtime?.services?.unauthorized
                      ? t('applications.detail.unauthorized')
                      : t('applications.detail.noNetwork')}
                  </div>
                )}
            </div>
          </div>

          {/* Consistency */}
          <div className="rounded-xl border bg-card px-5 py-4">
            <div className="mb-3 flex items-center gap-2">
              <span className="text-sm font-semibold">
                {t('applications.detail.consistency')}
              </span>
              {isConflict ? (
                <ApplicationStatusChip status="conflict" />
              ) : pending.length > 0 || statusReport?.status === 'merged' ? (
                <ApplicationStatusChip status="merged" />
              ) : (
                <ApplicationStatusChip status={status} />
              )}
              <span className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
                {statusReport?.checkedAt &&
                  t('applications.detail.checkedAt', {
                    time: formatDate(statusReport.checkedAt),
                  })}
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => void refetchStatus()}
                >
                  {t('common.actions.refresh')}
                </Button>
              </span>
            </div>

            {isConflict ? (
              <>
                <div className="grid gap-4 md:grid-cols-2">
                  <DiffPathsCard
                    tone="amber"
                    title={t('applications.detail.clusterSideChanges', {
                      count: drift.reduce(
                        (sum, docDiff) => sum + (docDiff.paths?.length ?? 1),
                        0
                      ),
                    })}
                    docs={drift}
                    footer={t('applications.detail.conflictSourceHint')}
                  />
                  <DiffPathsCard
                    tone="sky"
                    title={t('applications.detail.dbSideChanges', {
                      count: pending.reduce(
                        (sum, docDiff) => sum + (docDiff.paths?.length ?? 1),
                        0
                      ),
                    })}
                    docs={pending}
                    footer={
                      pending.length === 0
                        ? t('applications.detail.noPendingChanges')
                        : undefined
                    }
                  />
                </div>
                <div className="mt-3 flex flex-wrap items-center gap-2 border-t pt-3">
                  <span className="text-sm text-muted-foreground">
                    {t('applications.detail.conflictGate')}
                  </span>
                  <Button size="sm" className="ml-auto" onClick={onMerge}>
                    {t('applications.detail.resolveConflict')}
                  </Button>
                </div>
              </>
            ) : pending.length > 0 || statusReport?.status === 'merged' ? (
              <div className="space-y-3">
                {(drift.length > 0 || pending.length > 0) && (
                  <div className="grid gap-4 md:grid-cols-2">
                    {drift.length > 0 && (
                      <DiffPathsCard
                        tone="amber"
                        title={t('applications.detail.adoptedClusterChanges', {
                          count: drift.reduce(
                            (sum, docDiff) =>
                              sum + (docDiff.paths?.length ?? 1),
                            0
                          ),
                        })}
                        docs={drift}
                      />
                    )}
                    {pending.length > 0 && (
                      <DiffPathsCard
                        tone="sky"
                        title={t('applications.detail.pendingPublishChanges', {
                          count: pending.reduce(
                            (sum, docDiff) =>
                              sum + (docDiff.paths?.length ?? 1),
                            0
                          ),
                        })}
                        docs={pending}
                      />
                    )}
                  </div>
                )}
                <div className="flex items-center gap-3 border-t pt-3">
                  <span className="text-sm text-muted-foreground">
                    {t('applications.detail.mergedBanner')}
                  </span>
                  <Button size="sm" className="ml-auto" onClick={onDeploy}>
                    {t('applications.detail.goPublish')}
                  </Button>
                </div>
              </div>
            ) : (
              <div className="flex items-center gap-3">
                <span className="text-sm text-muted-foreground">
                  {t('applications.detail.syncedBanner')}
                </span>
                <Button
                  size="sm"
                  variant="outline"
                  className="ml-auto"
                  onClick={onViewLastApplied}
                >
                  {t('applications.detail.viewLastApplied')}
                </Button>
              </div>
            )}
          </div>

          {/* Warning events */}
          <div className="overflow-hidden rounded-xl border bg-card">
            <div className="border-b px-5 py-3 text-sm font-semibold">
              {t('applications.detail.events', {
                count: runtime?.events?.items.length ?? 0,
              })}
            </div>
            <div className="divide-y">
              {(runtime?.events?.items ?? []).map((event, index) => (
                <div
                  key={index}
                  className="flex items-center gap-3 px-5 py-2.5 text-sm"
                >
                  <Badge variant="destructive" className="shrink-0">
                    Warning
                  </Badge>
                  <span className="font-mono text-xs text-muted-foreground">
                    {event.reason || '—'}
                  </span>
                  <span className="text-muted-foreground">
                    {event.message || '—'}
                  </span>
                  <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                    {event.count
                      ? t('applications.detail.eventCount', {
                          count: event.count,
                        })
                      : ''}{' '}
                    {event.lastTimestamp ? formatDate(event.lastTimestamp) : ''}
                  </span>
                </div>
              ))}
              {(runtime?.events?.items.length ?? 0) === 0 && (
                <div className="px-5 py-6 text-center text-sm text-muted-foreground">
                  {runtime?.events?.unauthorized
                    ? t('applications.detail.unauthorized')
                    : t('applications.detail.noEvents')}
                </div>
              )}
            </div>
          </div>
        </>
      )}

      <PodMonitoringDialog
        appName={app.name}
        namespace={namespace}
        cluster={config.cluster}
        podName={podDialog.pod}
        open={podDialog.open}
        initialView={podDialog.view}
        onOpenChange={(nextOpen) =>
          setPodDialog((prev) => ({ ...prev, open: nextOpen }))
        }
        key={`${config.cluster}-${podDialog.pod}-${podDialog.view ?? ''}`}
      />
    </div>
  )
}
