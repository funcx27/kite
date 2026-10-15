import { useEffect, useMemo, useRef, useState } from 'react'
import {
  resolveApplicationConfig,
  restartApplicationConfig,
  restartApplicationPod,
  useApplicationConfig,
  useApplicationResources,
  useApplicationStatus,
} from '@/portal/application/api'
import { dumpDocYaml, type DocYamlDump } from '@/portal/application/lib'
import type {
  Application,
  ApplicationConfigSummary,
  ApplicationResolveAdoption,
  ApplicationStatusDoc,
} from '@/portal/application/types'
import { useQueryClient } from '@tanstack/react-query'
import { ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import { toast } from 'sonner'

import { formatBytes, formatDate, translateError } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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

import { ApplicationStatusChip } from './status-badge'
import { YamlDiffViewer } from './yaml-diff-viewer'

interface ClusterTabProps {
  app: Application
  config: ApplicationConfigSummary
  active: boolean
  onDeploy: () => void
  onOffline: () => void
  onHistory: () => void
  onCopyConfig: () => void
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

type SideChoice = 'db' | 'cluster'

function driftDocKey(doc: {
  kind: string
  namespace?: string
  name: string
}): string {
  return `${doc.kind}/${doc.namespace || ''}/${doc.name}`
}

// Anchor line for a path absent from one side's dump: the nearest preceding
// path in document order (per the other side, which still has it) that exists
// on this side; if none, the following path's line − 1; 0 when this side has
// no diff lines at all.
function anchorFor(
  missing: DocYamlDump,
  guide: DocYamlDump,
  path: string
): number {
  const order = guide.orderedPaths
  const at = order.indexOf(path)
  if (at < 0) return 0
  for (let i = at - 1; i >= 0; i--) {
    const line = missing.lineOfPath[order[i]]
    if (line) return line
  }
  for (let i = at + 1; i < order.length; i++) {
    const line = missing.lineOfPath[order[i]]
    if (line) return Math.max(0, line - 1)
  }
  return 0
}

// DriftDocCard renders one drift document row: opening it shows the
// live-vs-manifest diff view with per-path side-pick arrows on the changed
// lines themselves. One-sided documents (missingIn) explain themselves and
// carry no pick actions.
function DriftDocCard({
  doc,
  onView,
}: {
  doc: ApplicationStatusDoc
  onView: (doc: ApplicationStatusDoc) => void
}) {
  const { t } = useTranslation()
  // Note-only docs (certificate rotation notices) carry no diff sides: a
  // plain text row with nothing to open.
  if (doc.note && !doc.live && !doc.manifest) {
    return (
      <div className="flex w-full items-center gap-2 rounded-lg border p-3">
        <span className="font-mono text-xs font-medium">
          {doc.kind}/{doc.name}
        </span>
        <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
          {doc.note}
        </span>
      </div>
    )
  }
  const note = doc.missingIn
    ? doc.missingIn === 'cluster'
      ? t('applications.detail.docMissingInCluster')
      : t('applications.detail.docMissingInManifests')
    : t('applications.detail.pathsCount', { count: doc.paths?.length ?? 0 }) +
      (doc.truncated ? ` · ${t('applications.detail.pathsTruncated')}` : '')
  return (
    <button
      type="button"
      className="flex w-full items-center gap-2 rounded-lg border p-3 text-left transition-colors hover:bg-accent"
      onClick={() => onView(doc)}
    >
      <span className="font-mono text-xs font-medium">
        {doc.kind}/{doc.name}
      </span>
      <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
        {note}
      </span>
      <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
    </button>
  )
}

// One cluster tab of the detail page: overview bar → pod table → network
// card → consistency card (drift side-taking) → warning events.
export function ClusterTab({
  app,
  config,
  active,
  onDeploy,
  onOffline,
  onHistory,
  onCopyConfig,
}: ClusterTabProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { data: detail } = useApplicationConfig(
    active ? app.namespace : undefined,
    active ? app.name : undefined,
    active ? config.cluster : undefined
  )
  const { data: statusReport, refetch: refetchStatus } = useApplicationStatus(
    app.namespace,
    app.name,
    config.cluster,
    { disable: !active }
  )
  const { data: runtime } = useApplicationResources(
    app.namespace,
    app.name,
    config.cluster,
    {
      disable: !active,
      refreshInterval: active ? 5000 : 0,
    }
  )
  const navigate = useNavigate()
  const [restarting, setRestarting] = useState(false)
  // Two-step restart confirm: the target ('config' or a pod name) arms on
  // the first click and auto-disarms, guarding against accidental restarts.
  const [confirmTarget, setConfirmTarget] = useState<string | null>(null)
  const confirmTimer = useRef<number | undefined>(undefined)

  // Drift side-taking: per "<docKey>:<path>" picks, defaulting to the DB
  // side; reset whenever a fresh report replaces the diff behind them.
  const [choices, setChoices] = useState<Record<string, SideChoice>>({})
  const [viewDoc, setViewDoc] = useState<ApplicationStatusDoc | null>(null)
  const [resolving, setResolving] = useState(false)
  // Resolve writes a revision, so the apply button routes through a confirm
  // dialog that spells out the side split before committing.
  const [confirmApply, setConfirmApply] = useState(false)

  useEffect(() => () => window.clearTimeout(confirmTimer.current), [])

  useEffect(() => setChoices({}), [statusReport])

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
  // Offlined = undeployed (no manifests to diff); nothing else signals it.
  const isOfflined = status === 'undeployed'

  const workload = runtime?.workload
  const workloadUnauthorized = !workload || workload.unauthorized

  // Paths switched to the cluster side, grouped per document — the resolve
  // payload. The report's previewToken pins the diff these picks decided on.
  const adoptions: ApplicationResolveAdoption[] = useMemo(
    () =>
      (statusReport?.docs ?? [])
        .map((doc) => ({
          kind: doc.kind,
          namespace: doc.namespace,
          name: doc.name,
          paths: (doc.paths ?? []).filter(
            (path) => choices[`${driftDocKey(doc)}:${path}`] === 'cluster'
          ),
        }))
        .filter((adoption) => adoption.paths.length > 0),
    [statusReport, choices]
  )
  const canApply =
    adoptions.length > 0 &&
    !!statusReport?.previewToken &&
    !!detail &&
    !resolving

  // Pick split across the report's changed paths: cluster picks are what the
  // resolve request adopts; db picks keep the stored value (no write), so the
  // apply button names both sides to stay unambiguous.
  const pickCounts = useMemo(() => {
    let cluster = 0
    let db = 0
    for (const doc of statusReport?.docs ?? []) {
      for (const path of doc.paths ?? []) {
        if (choices[`${driftDocKey(doc)}:${path}`] === 'cluster') cluster++
        else db++
      }
    }
    return { cluster, db }
  }, [statusReport, choices])

  const pickSide = (key: string, value: SideChoice) =>
    setChoices((prev) => ({ ...prev, [key]: value }))

  // The open document rendered through our own deterministic emitter: the
  // path→line map pins each side-pick widget to its changed line. raw content
  // — the viewer must not re-dump, that would reflow the lines.
  const docDump = useMemo(
    () =>
      viewDoc
        ? {
            live: dumpDocYaml(viewDoc.live),
            manifest: dumpDocYaml(viewDoc.manifest),
          }
        : null,
    [viewDoc]
  )
  const lineWidgets = useMemo(() => {
    if (!viewDoc || !docDump) return []
    const docKey = driftDocKey(viewDoc)
    return (viewDoc.paths ?? [])
      .map((path) => {
        const manifestLine =
          docDump.manifest.lineOfPath[path] ??
          anchorFor(docDump.manifest, docDump.live, path)
        const liveLine =
          docDump.live.lineOfPath[path] ??
          anchorFor(docDump.live, docDump.manifest, path)
        return {
          path,
          originalLine: liveLine,
          modifiedLine: manifestLine,
          picked: choices[`${docKey}:${path}`] ?? 'db',
          dbTitle: t('applications.detail.useDb'),
          clusterTitle: t('applications.detail.useCluster'),
          onPick: (picked: SideChoice) => pickSide(`${docKey}:${path}`, picked),
        }
      })
      .filter((widget) => widget.originalLine > 0 || widget.modifiedLine > 0)
  }, [viewDoc, docDump, choices, pickSide, t])

  const handleResolve = async () => {
    if (!statusReport?.previewToken || !detail) return
    setResolving(true)
    try {
      await resolveApplicationConfig(app.namespace, app.name, config.cluster, {
        expectedRev: detail.latestRev,
        previewToken: statusReport.previewToken,
        adoptions,
      })
      toast.success(t('applications.detail.resolveApplied'))
      setChoices({})
      setViewDoc(null)
      await queryClient.invalidateQueries({
        queryKey: [
          'applications',
          app.namespace,
          app.name,
          'config',
          config.cluster,
        ],
      })
      await refetchStatus()
    } catch (err) {
      toast.error(translateError(err, t))
      // A stale previewToken or a concurrent save (409) means the diff the
      // picks were made on is gone; reload the report for a fresh decision.
      await refetchStatus()
    } finally {
      setResolving(false)
      setConfirmApply(false)
    }
  }

  const handleRestart = async () => {
    setRestarting(true)
    try {
      await restartApplicationConfig(app.namespace, app.name, config.cluster)
      toast.success(
        t('applications.detail.restartStarted', { cluster: config.cluster })
      )
      await queryClient.invalidateQueries({
        queryKey: [
          'applications',
          app.namespace,
          app.name,
          'resources',
          config.cluster,
        ],
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
      await restartApplicationPod(
        app.namespace,
        app.name,
        config.cluster,
        podName
      )
      toast.success(t('applications.detail.podRestarted', { pod: podName }))
      await queryClient.invalidateQueries({
        queryKey: [
          'applications',
          app.namespace,
          app.name,
          'resources',
          config.cluster,
        ],
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
                          navigate(
                            `/applications/${app.namespace}/${app.name}/clusters/${config.cluster}/pods/${pod.name}`
                          )
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
              <ApplicationStatusChip status={status} />
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

            {status === 'error' ? (
              <div className="text-sm text-destructive">
                {statusReport?.error || t('applications.status.error')}
              </div>
            ) : (status === 'drift' || status === 'pending') && statusReport ? (
              <div className="space-y-2">
                {statusReport.docs.map((doc) => (
                  <DriftDocCard
                    key={driftDocKey(doc)}
                    doc={doc}
                    onView={setViewDoc}
                  />
                ))}
              </div>
            ) : (
              <div className="flex items-center gap-3">
                <span className="text-sm text-muted-foreground">
                  {status === 'synced'
                    ? t('applications.detail.syncedBanner')
                    : t('applications.detail.checkingStatus')}
                </span>
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

      {/* Selected document's live vs manifest view: pane badges name the two
          sides, each changed path's ← → pair floats over the diff's center
          seam (← adopts the cluster value, → the DB value), and the compact
          pick-all control sits at the seam's top; the apply action stays in
          the dialog header. */}
      {viewDoc && (
        <YamlDiffViewer
          open={true}
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setViewDoc(null)
          }}
          original={docDump?.live.text ?? ''}
          modified={docDump?.manifest.text ?? ''}
          raw
          lineWidgets={lineWidgets}
          pickAllLabels={{
            cluster: t('applications.detail.useClusterAll'),
            db: t('applications.detail.useDbAll'),
          }}
          paneLabels={{
            original: t('applications.detail.clusterLive'),
            modified: t('applications.detail.dbConfig'),
          }}
          title={`${viewDoc.kind}/${viewDoc.name}`}
          hideUnchanged={true}
          height={420}
          action={
            <Button
              size="sm"
              disabled={!canApply}
              title={t('applications.detail.applySelectionHint')}
              onClick={() => setConfirmApply(true)}
            >
              {t('applications.detail.applySelectionCounts', {
                cluster: pickCounts.cluster,
                db: pickCounts.db,
              })}
            </Button>
          }
        />
      )}

      {/* Resolve confirm: the side split behind the apply button is spelled
          out before the write commits. */}
      <Dialog open={confirmApply} onOpenChange={setConfirmApply}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>
              {t('applications.detail.applyConfirmTitle')}
            </DialogTitle>
            <DialogDescription>
              {t('applications.detail.applyConfirmBody', {
                cluster: pickCounts.cluster,
                db: pickCounts.db,
              })}
            </DialogDescription>
          </DialogHeader>
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={() => setConfirmApply(false)}>
              {t('common.actions.cancel')}
            </Button>
            <Button disabled={resolving} onClick={() => void handleResolve()}>
              {t('applications.detail.applyConfirmAction')}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}
