import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import type {
  Application,
  ApplicationConfig,
  ApplicationConflict,
} from '@/types/application'
import {
  ApplicationApiError,
  createApplicationConfig,
  deployApplicationConfig,
  useApplicationConfig,
  useApplicationStatus,
} from '@/lib/api/applications'
import { applicationStatusLabel } from '@/lib/applications'
import { formatDate, translateError } from '@/lib/utils'
import { useCluster } from '@/hooks/use-cluster'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { ApplicationStatusChip } from './status-badge'

interface DeployDialogProps {
  app: Application
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Publish an existing config (detail page cluster tab) */
  cluster?: string
  /** First publish: the app has no cluster config yet */
  firstPublish?: boolean
  /** Resolved after first-publish cluster selection */
  onConfigCreated?: (config: ApplicationConfig) => void
  /** Opens the wizard for [修改配置] */
  onEditConfig?: () => void
  /** Opens the merge preview for the conflict state */
  onResolveConflict?: () => void
}

interface DryRunState {
  loading: boolean
  error: string | null
  creates: Array<{ kind: string; name: string }>
  updates: Array<{ kind: string; name: string }>
  changeCount: number
}

// The deploy dialog is the single entry for modify + publish (design §11):
// quick image edit, [修改配置] via the wizard, a server-side dry-run preview,
// and the conflict state that only offers the three-way merge.
export function DeployDialog({
  app,
  open,
  onOpenChange,
  cluster,
  firstPublish = false,
  onConfigCreated,
  onEditConfig,
  onResolveConflict,
}: DeployDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { clusters } = useCluster()

  const [targetCluster, setTargetCluster] = useState<string | undefined>(
    cluster
  )
  // Config created by a first-publish cluster selection; otherwise the
  // config comes from the query below.
  const [createdConfig, setCreatedConfig] = useState<
    ApplicationConfig | undefined
  >(undefined)
  const [creating, setCreating] = useState(false)
  const [image, setImage] = useState('')
  const [deploying, setDeploying] = useState(false)
  const [conflicts, setConflicts] = useState<ApplicationConflict[] | null>(null)
  const [dryRun, setDryRun] = useState<DryRunState>({
    loading: false,
    error: null,
    creates: [],
    updates: [],
    changeCount: 0,
  })
  // Tracks the last config-seeded image so the sync effect never clobbers a
  // user edit on a background refetch.
  const imageBaselineRef = useRef('')

  // Reset per open / target change. The image baseline is the cluster
  // config's image (never the retired birth draft) — an unprompted
  // difference must not turn into a publish (design §6: 改镜像即发布).
  useEffect(() => {
    if (open) {
      setTargetCluster(cluster)
      setCreatedConfig(undefined)
      setConflicts(null)
      setImage('')
    }
  }, [open, cluster])

  const activeCluster = targetCluster ?? cluster
  const { data: statusReport } = useApplicationStatus(app.name, activeCluster, {
    disable: !open || !activeCluster || (firstPublish && !targetCluster),
  })
  const configNeeded = open && !!activeCluster && !firstPublish
  const { data: fetchedConfig } = useApplicationConfig(
    configNeeded ? app.name : undefined,
    configNeeded ? activeCluster : undefined
  )
  const config = createdConfig ?? fetchedConfig

  // The cluster config is the image baseline; sync while the user has not
  // typed an edit of their own. `open` is a dependency on purpose: reopening
  // clears the field, and a warm unchanged cache hands back the same config
  // object reference, which alone would never re-seed it.
  useEffect(() => {
    if (open && config) {
      setImage((prev) =>
        prev === '' || prev === imageBaselineRef.current
          ? (config.spec?.workload.image ?? '')
          : prev
      )
      imageBaselineRef.current = config.spec?.workload.image ?? ''
    }
  }, [open, config])

  // First publish has no cluster config yet: seed the image from the birth
  // draft (App.Spec) — the same source the first Render uses (design §3/§6).
  const draftImage = app.spec?.workload.image ?? ''
  useEffect(() => {
    if (open && firstPublish && !config) {
      setImage((prev) => (prev === '' ? draftImage : prev))
    }
  }, [open, firstPublish, config, draftImage])

  // GET /clusters only returns RBAC-visible clusters; offline ones carry
  // `error`. It has no `connected` field — that exists on the admin
  // cluster-management list only, so filtering on it empties the dropdown.
  const availableClusters = useMemo(
    () => clusters.filter((candidate) => !candidate.error),
    [clusters]
  )

  const drift = statusReport?.drift ?? []
  // Only the gate verdict (action=merge) or a real 409 blocks publishing;
  // drift alone stays non-empty in the merged state until the publish lands.
  const isConflict = statusReport?.action === 'merge' || conflicts !== null

  const originalImage =
    config?.spec?.workload.image ?? (firstPublish ? draftImage : '')
  const imageChanged = image.trim() !== '' && image.trim() !== originalImage

  // Server-side dry-run preview. The pending image rides the preview too —
  // the backend injects it into an in-memory manifests copy for validation,
  // so previews never write revisions (design §6).
  const runDryRun = useCallback(async () => {
    if (!activeCluster || (firstPublish && !config)) {
      // First publish before the cluster config exists: nothing to
      // dry-run yet; the create-mode preview takes over after selection.
      setDryRun({
        loading: false,
        error: null,
        creates: [],
        updates: [],
        changeCount: -1,
      })
      return
    }
    setDryRun((prev) => ({ ...prev, loading: true, error: null }))
    try {
      const result = await deployApplicationConfig(app.name, activeCluster, {
        dryRun: true,
        image: imageChanged ? image.trim() : undefined,
      })
      const creates = result.results
        .filter((docResult) => docResult.action === 'create')
        .map((docResult) => ({ kind: docResult.kind, name: docResult.name }))
      const updates = result.results
        .filter((docResult) => docResult.action === 'update')
        .map((docResult) => ({ kind: docResult.kind, name: docResult.name }))
      const changeCount = (result.change ?? []).reduce(
        (sum, docDiff) => sum + (docDiff.paths?.length ?? 0),
        0
      )
      setDryRun({
        loading: false,
        error: null,
        creates,
        updates,
        changeCount,
      })
    } catch (err) {
      if (err instanceof ApplicationApiError && err.status === 409) {
        setConflicts(
          (err.payload.conflicts as ApplicationConflict[] | undefined) ?? []
        )
      }
      setDryRun({
        loading: false,
        error: translateError(err, t),
        creates: [],
        updates: [],
        changeCount: 0,
      })
    }
  }, [activeCluster, app.name, firstPublish, config, t, image, imageChanged])

  useEffect(() => {
    if (!open || isConflict) return
    const timer = setTimeout(() => void runDryRun(), 300)
    return () => clearTimeout(timer)
    // Re-validate when the image input settles or the config appears.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, activeCluster, image, isConflict, config])

  // A merged config may have nothing to push (pure adoption or legacy state):
  // publishing is the no-op apply that advances the consistency anchor.
  const mergedPending = statusReport?.status === 'merged'
  const hasChanges =
    firstPublish ||
    imageChanged ||
    mergedPending ||
    dryRun.changeCount > 0 ||
    dryRun.creates.length > 0

  const canConfirm =
    !isConflict &&
    !dryRun.loading &&
    !dryRun.error &&
    hasChanges &&
    !!activeCluster &&
    !deploying

  const pickFirstPublishCluster = async (nextCluster: string) => {
    setTargetCluster(nextCluster)
    setCreating(true)
    try {
      const created = await createApplicationConfig(app.name, {
        cluster: nextCluster,
      })
      setCreatedConfig(created)
      onConfigCreated?.(created)
      toast.success(
        t('applications.deploy.configCreated', { cluster: nextCluster })
      )
    } catch (err) {
      toast.error(translateError(err, t))
      setTargetCluster(undefined)
    } finally {
      setCreating(false)
    }
  }

  const handleDeploy = async () => {
    if (!activeCluster) return
    setDeploying(true)
    try {
      const result = await deployApplicationConfig(app.name, activeCluster, {
        image: imageChanged ? image.trim() : undefined,
      })
      if (result.success) {
        toast.success(
          t('applications.deploy.success', { cluster: activeCluster })
        )
      } else {
        toast.warning(
          t('applications.deploy.partialFailure', {
            failed: result.results.filter((docResult) => !docResult.success)
              .length,
          })
        )
      }
      await queryClient.invalidateQueries({ queryKey: ['applications'] })
      onOpenChange(false)
    } catch (err) {
      if (err instanceof ApplicationApiError && err.status === 409) {
        setConflicts(
          (err.payload.conflicts as ApplicationConflict[] | undefined) ?? []
        )
      } else {
        toast.error(translateError(err, t))
      }
    } finally {
      setDeploying(false)
    }
  }

  const summary = useMemo(() => {
    // First publish before the cluster config exists: summarize the birth
    // draft so the dialog doesn't show an empty config row.
    const spec = config?.spec ?? (firstPublish ? app.spec : undefined)
    if (!spec) return ''
    const workload = spec.workload
    const parts = [
      t('applications.deploy.summaryReplicas', {
        count: workload?.replicas ?? 0,
      }),
      t('applications.deploy.summaryEnv', {
        count: workload?.env?.length ?? 0,
      }),
      t('applications.deploy.summaryPorts', {
        count: spec.ports?.length ?? 0,
      }),
      t('applications.deploy.summaryFiles', {
        count: spec.configFiles?.length ?? 0,
      }),
    ]
    return parts.join(' · ')
  }, [config, firstPublish, app.spec, t])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {firstPublish && !targetCluster
              ? t('applications.deploy.firstPublishTitle', { name: app.name })
              : t('applications.deploy.title', {
                  namespace: config?.namespace ?? app.namespace,
                  name: app.name,
                  cluster: activeCluster ?? '',
                })}
          </DialogTitle>
          <DialogDescription className="sr-only">
            {t('applications.deploy.title')}
          </DialogDescription>
        </DialogHeader>

        {isConflict ? (
          <div className="space-y-3">
            <div className="flex items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2.5 text-xs text-destructive">
              <span>⛔</span>
              <span>{t('applications.deploy.conflictBanner')}</span>
            </div>
            <div className="rounded-lg border border-amber-200 bg-amber-50/40 p-3 dark:border-amber-800 dark:bg-amber-950/40">
              <div className="text-xs font-semibold text-amber-700 dark:text-amber-400">
                {t('applications.deploy.clusterSideChanges', {
                  count:
                    conflicts?.reduce(
                      (sum, conflict) => sum + conflict.paths.length,
                      0
                    ) ??
                    drift.reduce(
                      (sum, docDiff) => sum + (docDiff.paths?.length ?? 0),
                      0
                    ),
                })}
              </div>
              <div className="mt-1.5 space-y-1.5">
                {(conflicts ?? drift).map((conflict) =>
                  (conflict.paths ?? []).map((path) => (
                    <div
                      key={`${conflict.kind}/${conflict.name}:${path}`}
                      className="font-mono text-xs text-muted-foreground"
                    >
                      {conflict.kind}/{conflict.name} · {path}
                    </div>
                  ))
                )}
              </div>
              <div className="mt-1 text-xs text-muted-foreground">
                {t('applications.deploy.conflictSourceHint')}
              </div>
            </div>
            <Button
              className="w-full"
              onClick={() => {
                onOpenChange(false)
                onResolveConflict?.()
              }}
            >
              {t('applications.deploy.resolveConflict')}
            </Button>
          </div>
        ) : (
          <div className="space-y-4">
            {firstPublish && !targetCluster && (
              <div>
                <Label>{t('applications.deploy.targetCluster')}</Label>
                <Select
                  value={targetCluster ?? ''}
                  onValueChange={(value) => void pickFirstPublishCluster(value)}
                  disabled={creating}
                >
                  <SelectTrigger className="mt-1.5">
                    <SelectValue
                      placeholder={t('applications.deploy.pickCluster')}
                    />
                  </SelectTrigger>
                  <SelectContent>
                    {availableClusters.map((candidate) => (
                      <SelectItem key={candidate.name} value={candidate.name}>
                        {candidate.name} · {candidate.version ?? ''}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className="mt-1 text-xs text-muted-foreground">
                  {t('applications.deploy.targetClusterHint')}
                </p>
              </div>
            )}

            <div>
              <Label>{t('applications.deploy.image')}</Label>
              <Input
                value={image}
                onChange={(event) => setImage(event.target.value)}
                className="mt-1.5 font-mono text-sm"
                disabled={
                  !activeCluster || creating || (!firstPublish && !config)
                }
              />
              <p className="mt-1 text-xs text-muted-foreground">
                {t('applications.deploy.imageHint')}
              </p>
            </div>

            <div>
              <Label>{t('applications.deploy.otherConfig')}</Label>
              <div className="mt-1.5 flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2.5">
                <span className="text-xs text-muted-foreground">{summary}</span>
                {activeCluster && config && (
                  <ApplicationStatusChip
                    status={statusReport?.status ?? config.statusSummary}
                  />
                )}
                <Button
                  size="sm"
                  variant="outline"
                  className="ml-auto"
                  disabled={!activeCluster}
                  onClick={() => {
                    onOpenChange(false)
                    onEditConfig?.()
                  }}
                >
                  {t('applications.deploy.editConfig')}
                </Button>
              </div>
            </div>

            <div>
              <Label>{t('applications.deploy.changePreview')}</Label>
              <div className="mt-1.5 rounded-lg bg-zinc-950 p-3 font-mono text-xs leading-5 text-zinc-300">
                {dryRun.loading ? (
                  <span className="text-zinc-500">
                    {t('applications.deploy.dryRunRunning')}
                  </span>
                ) : dryRun.error ? (
                  <span className="text-red-400">{dryRun.error}</span>
                ) : firstPublish && !config ? (
                  <span className="text-zinc-500">
                    {t('applications.deploy.createModeHint')}
                  </span>
                ) : dryRun.creates.length > 0 && dryRun.updates.length === 0 ? (
                  <div className="space-y-0.5">
                    <div className="text-zinc-500">
                      #{' '}
                      {t('applications.deploy.createModeDocs', {
                        count: dryRun.creates.length,
                      })}
                    </div>
                    {dryRun.creates.map((doc) => (
                      <div key={`${doc.kind}/${doc.name}`}>
                        <span className="text-sky-300">create</span> {doc.kind}/
                        {doc.name}
                        {imageChanged && (
                          <span className="text-zinc-500">
                            {' '}
                            · image → {image.trim()}
                          </span>
                        )}
                      </div>
                    ))}
                  </div>
                ) : hasChanges ? (
                  <div className="space-y-0.5">
                    {dryRun.updates.map((doc) => (
                      <div key={`${doc.kind}/${doc.name}`}>
                        <span className="text-emerald-300">update</span>{' '}
                        {doc.kind}/{doc.name}
                      </div>
                    ))}
                    {imageChanged && (
                      <div>
                        <span className="text-emerald-300">update</span> image:{' '}
                        {originalImage || '—'} → {image.trim()}
                      </div>
                    )}
                    {dryRun.changeCount > 0 && (
                      <div className="text-zinc-500">
                        #{' '}
                        {t('applications.deploy.changePaths', {
                          count: dryRun.changeCount,
                        })}
                      </div>
                    )}
                  </div>
                ) : (
                  <span className="text-zinc-500">
                    {t('applications.deploy.noChanges')}
                  </span>
                )}
              </div>
              {!dryRun.loading && !dryRun.error && canConfirm && (
                <div className="mt-2 flex items-center gap-1.5 text-xs text-green-600">
                  <span>✔</span>
                  <span>
                    {firstPublish
                      ? t('applications.deploy.dryRunOkCreate')
                      : t('applications.deploy.dryRunOk')}
                  </span>
                </div>
              )}
            </div>

            <div className="flex items-start gap-2 rounded-lg bg-muted/50 px-3 py-2.5 text-xs text-muted-foreground">
              <span>ℹ</span>
              <span>{t('applications.deploy.anchorHint')}</span>
            </div>

            {config?.lastDeployedAt && (
              <div className="flex items-center gap-2 text-xs text-muted-foreground">
                {t('applications.deploy.lastDeployed', {
                  time: formatDate(config.lastDeployedAt),
                })}
                {config.lastSyncedAt && (
                  <>
                    ·{' '}
                    {t('applications.deploy.lastSynced', {
                      time: formatDate(config.lastSyncedAt),
                    })}
                  </>
                )}
                {statusReport?.checkedAt && (
                  <>· {applicationStatusLabel(statusReport.status, t)}</>
                )}
              </div>
            )}
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.actions.cancel')}
          </Button>
          {!isConflict && (
            <Button disabled={!canConfirm} onClick={() => void handleDeploy()}>
              {firstPublish
                ? t('applications.deploy.confirmFirstPublish')
                : t('applications.deploy.confirmPublish')}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
