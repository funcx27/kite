import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  ApplicationApiError,
  deployApplicationConfig,
  useApplicationConfig,
  useApplicationStatus,
} from '@/portal/application/api'
import { applicationStatusLabel } from '@/portal/application/lib'
import type {
  Application,
  ApplicationDeployDocPreview,
  ApplicationDocDiff,
  ApplicationPendingChange,
} from '@/portal/application/types'
import { useSpaces } from '@/portal/space-context'
import { useCredentials } from '@/portal/workflow/api'
import { useQueryClient } from '@tanstack/react-query'
import * as yaml from 'js-yaml'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

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
import { YamlDiffViewer } from './yaml-diff-viewer'

interface DeployDialogProps {
  app: Application
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Publish an existing config (detail page cluster tab) */
  cluster?: string
  /** First publish: the app has no cluster config yet */
  firstPublish?: boolean
  /** Copy-create mode: deep-copy this cluster's config as the base of the
   *  target's first publish (confirm request carries copyFrom). */
  createCopyFrom?: string
  /** Opens the wizard for [修改配置] */
  onEditConfig?: () => void
  /** Wizard edits carried into this dialog: persisted and published by the
   *  same [确认发布] request; cancelling discards them. */
  pending?: ApplicationPendingChange
}

interface DryRunState {
  loading: boolean
  error: string | null
  creates: Array<{ kind: string; name: string }>
  updates: Array<{ kind: string; name: string }>
  change: ApplicationDocDiff[]
  changeCount: number
  preview: ApplicationDeployDocPreview[]
  /** Dry-run response token; [确认发布] echoes it back so the server can
   *  reject the push (409) when the cluster moved on after the preview. */
  previewToken?: string
}

const idleDryRun: DryRunState = {
  loading: false,
  error: null,
  creates: [],
  updates: [],
  change: [],
  changeCount: 0,
  preview: [],
}

// Sentinel for "no pull credential" in the Select (radix Select cannot hold
// an empty-string item value); sent to the backend as the empty string.
const pullCredentialNone = '__none__'

// The deploy dialog is the single entry for modify + publish: quick image
// edit, pending wizard edits, and a server-side dry-run preview with
// expandable field-level detail. Persisting a revision and applying it happen
// together on [确认发布] — never before. Create modes (first publish /
// copy-create) also go through here: the target config does not exist until
// the confirm request creates it and publishes in one chain.
export function DeployDialog({
  app,
  open,
  onOpenChange,
  cluster,
  firstPublish = false,
  createCopyFrom,
  onEditConfig,
  pending,
}: DeployDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { clusters } = useCluster()
  const { spaceClusters } = useSpaces()

  const [targetCluster, setTargetCluster] = useState<string | undefined>(
    cluster
  )
  const [image, setImage] = useState('')
  const [deploying, setDeploying] = useState(false)
  const [pullCredential, setPullCredential] = useState(pullCredentialNone)
  const [diffDoc, setDiffDoc] = useState<{
    kind: string
    name: string
  } | null>(null)
  const [dryRun, setDryRun] = useState<DryRunState>(idleDryRun)
  // Tracks the last config-seeded image so the sync effect never clobbers a
  // user edit on a background refetch.
  const imageBaselineRef = useRef('')
  // Same guard for the pull credential: seeded from the config only while the
  // user has not picked one themselves.
  const pullCredentialTouchedRef = useRef(false)

  // Create mode: the target cluster has no config row yet — the backend
  // builds the base manifests (rendered from App.Spec, or deep-copied from
  // copyFrom) and creates config + first publish in one request.
  const copyMode = !!createCopyFrom
  const isCreateMode = firstPublish || copyMode

  // Reset per open / target change. The image baseline is the pending
  // wizard spec or the cluster config's image (never the retired birth
  // draft) — an unprompted difference must not turn into a publish
  // (design §6: 改镜像即发布).
  useEffect(() => {
    if (open) {
      setTargetCluster(cluster)
      setDiffDoc(null)
      setImage('')
      setPullCredential(pullCredentialNone)
      setDryRun(idleDryRun)
      pullCredentialTouchedRef.current = false
    }
  }, [open, cluster])

  // Pending wizard edits ride every dry-run and the deploy itself: the
  // backend persists them as a revision and applies them in one request.
  const pendingRequest = useMemo(
    () =>
      pending
        ? {
            spec: pending.spec,
            manifests: pending.manifests,
            expectedRev: pending.expectedRev,
          }
        : {},
    [pending]
  )

  const activeCluster = targetCluster ?? cluster
  // No status report in create mode: the target config does not exist yet.
  const { data: statusReport } = useApplicationStatus(
    app.namespace,
    app.name,
    activeCluster,
    {
      disable: !open || !activeCluster || isCreateMode,
    }
  )
  const configNeeded = open && !!activeCluster && !isCreateMode
  const { data: fetchedConfig } = useApplicationConfig(
    configNeeded ? app.namespace : undefined,
    configNeeded ? app.name : undefined,
    configNeeded ? activeCluster : undefined
  )
  // Copy-create: the source cluster's config supplies the image/credential
  // prefill and the summary; the target config does not exist until the
  // publish lands.
  const { data: copySourceConfig } = useApplicationConfig(
    open && copyMode ? app.namespace : undefined,
    open && copyMode ? app.name : undefined,
    open && copyMode ? createCopyFrom : undefined
  )
  const config = fetchedConfig ?? copySourceConfig

  // Seed the pull credential once the cluster config is known; a user pick
  // wins over the stored value for the lifetime of the dialog.
  const storedCredentialRef = config?.credentialRef ?? ''
  useEffect(() => {
    if (open && config && !pullCredentialTouchedRef.current) {
      setPullCredential(storedCredentialRef || pullCredentialNone)
    }
  }, [open, config, storedCredentialRef])

  // The pending wizard spec (or the cluster config) is the image baseline;
  // sync while the user has not typed an edit of their own. `open` is a
  // dependency on purpose: reopening clears the field, and a warm unchanged
  // cache hands back the same config object reference, which alone would
  // never re-seed it.
  const baselineImage =
    pending?.spec?.workload.image ?? config?.spec?.workload.image ?? ''
  useEffect(() => {
    if (open && config) {
      setImage((prev) =>
        prev === '' || prev === imageBaselineRef.current ? baselineImage : prev
      )
      imageBaselineRef.current = baselineImage
    }
  }, [open, config, baselineImage])

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
  // The app's namespace is a space, so only clusters where that space exists
  // are valid targets. Create modes also skip clusters the app is already
  // bound to (the copy source itself is bound, hence never a target).
  const availableClusters = useMemo(() => {
    const spaceClusterNames = new Set(spaceClusters[app.namespace] ?? [])
    return clusters.filter(
      (candidate) =>
        !candidate.error &&
        spaceClusterNames.has(candidate.name) &&
        !app.configs.some((config) => config.cluster === candidate.name)
    )
  }, [clusters, app.configs, app.namespace, spaceClusters])

  const { data: credentials } = useCredentials()
  // Pull secrets are built from registry entries only, so git-only
  // credentials are not valid candidates here.
  const credentialNames = useMemo(
    () =>
      (credentials ?? [])
        .filter((credential) => credential.registryHosts.length > 0)
        .map((credential) => credential.name),
    [credentials]
  )
  // Echo a stored credential that no longer exists so the Select can display
  // it instead of silently falling back to "none".
  const credentialEcho =
    storedCredentialRef && !credentialNames.includes(storedCredentialRef)
      ? storedCredentialRef
      : undefined

  const changePullCredential = (value: string) => {
    pullCredentialTouchedRef.current = true
    setPullCredential(value)
  }

  const pullCredentialRef =
    pullCredential === pullCredentialNone ? '' : pullCredential

  const originalImage =
    pending?.spec?.workload.image ??
    config?.spec?.workload.image ??
    (firstPublish ? draftImage : '')
  const imageChanged = image.trim() !== '' && image.trim() !== originalImage

  // Server-side dry-run preview of the exact set [确认发布] would push: the
  // pending wizard edits and image ride the preview and are only injected in
  // memory, so previews never write revisions (design §6). Create modes run
  // it too once a target cluster is picked — the backend renders/copies the
  // base manifests in memory. The response's previewToken pins this diff for
  // the confirm request.
  const runDryRun = useCallback(async () => {
    if (!activeCluster) return
    setDryRun((prev) => ({ ...prev, loading: true, error: null }))
    try {
      const result = await deployApplicationConfig(
        app.namespace,
        app.name,
        activeCluster,
        {
          dryRun: true,
          image: imageChanged ? image.trim() : undefined,
          credentialRef: pullCredentialRef,
          copyFrom: createCopyFrom,
          ...pendingRequest,
        }
      )
      const creates = result.results
        .filter((docResult) => docResult.action === 'create')
        .map((docResult) => ({ kind: docResult.kind, name: docResult.name }))
      const updates = result.results
        .filter((docResult) => docResult.action === 'update')
        .map((docResult) => ({ kind: docResult.kind, name: docResult.name }))
      const change = result.change ?? []
      const changeCount = change.reduce(
        (sum, docDiff) => sum + (docDiff.paths?.length ?? 0),
        0
      )
      setDryRun({
        loading: false,
        error: null,
        creates,
        updates,
        change,
        changeCount,
        preview: result.preview ?? [],
        previewToken: result.previewToken,
      })
    } catch (err) {
      setDryRun({
        loading: false,
        error: translateError(err, t),
        creates: [],
        updates: [],
        change: [],
        changeCount: 0,
        preview: [],
      })
    }
  }, [
    activeCluster,
    app.namespace,
    app.name,
    createCopyFrom,
    t,
    image,
    imageChanged,
    pullCredentialRef,
    pendingRequest,
  ])

  useEffect(() => {
    if (!open) return
    const timer = setTimeout(() => void runDryRun(), 300)
    return () => clearTimeout(timer)
    // Re-validate when the image input settles or the config appears.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, activeCluster, image, config, pending])

  // Publishing needs a real change to push (create modes always have one).
  const hasChanges =
    isCreateMode ||
    imageChanged ||
    dryRun.changeCount > 0 ||
    dryRun.creates.length > 0

  const canConfirm =
    !dryRun.loading &&
    !dryRun.error &&
    hasChanges &&
    !!activeCluster &&
    !deploying

  // 仅保存 persists what the preview shows — the pending wizard edits and a
  // bare image edit — as a revision without applying it to the cluster; the
  // pull credential does not ride a save, it lands with the publish.
  const canSave =
    !isCreateMode &&
    !!activeCluster &&
    !deploying &&
    !dryRun.loading &&
    !dryRun.error &&
    (!!pending || imageChanged)

  const handleSaveOnly = async () => {
    if (!activeCluster) return
    setDeploying(true)
    try {
      await deployApplicationConfig(app.namespace, app.name, activeCluster, {
        saveOnly: true,
        image: imageChanged ? image.trim() : undefined,
        ...pendingRequest,
      })
      toast.success(t('applications.deploy.savedNotPublished'))
      await queryClient.invalidateQueries({ queryKey: ['applications'] })
      onOpenChange(false)
    } catch (err) {
      toast.error(translateError(err, t))
      // A concurrent save (409) invalidated the revision the edits were
      // based on; reload for a fresh decision.
      if (err instanceof ApplicationApiError && err.status === 409) {
        await queryClient.invalidateQueries({ queryKey: ['applications'] })
        void runDryRun()
      }
    } finally {
      setDeploying(false)
    }
  }

  const handleDeploy = async () => {
    if (!activeCluster) return
    setDeploying(true)
    try {
      const result = await deployApplicationConfig(
        app.namespace,
        app.name,
        activeCluster,
        {
          image: imageChanged ? image.trim() : undefined,
          credentialRef: pullCredentialRef,
          copyFrom: copyMode ? createCopyFrom : undefined,
          previewToken: dryRun.previewToken,
          ...pendingRequest,
        }
      )
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
        // The previewToken no longer matches the cluster diff (or the config
        // moved underneath): reload the preview for a fresh decision.
        toast.info(t('applications.deploy.previewStale'))
        await queryClient.invalidateQueries({ queryKey: ['applications'] })
        void runDryRun()
      } else if (err instanceof ApplicationApiError && err.status === 422) {
        // API-level no-change gate: the set matches the newest revision and
        // the cluster already runs it.
        toast.info(t('applications.deploy.noChanges'))
        await queryClient.invalidateQueries({ queryKey: ['applications'] })
        onOpenChange(false)
      } else {
        toast.error(translateError(err, t))
      }
    } finally {
      setDeploying(false)
    }
  }

  const summary = useMemo(() => {
    // First publish before the cluster config exists: summarize the birth
    // draft so the dialog doesn't show an empty config row. Pending wizard
    // edits describe what [确认发布] will actually save.
    const spec =
      pending?.spec ?? config?.spec ?? (firstPublish ? app.spec : undefined)
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
  }, [pending, config, firstPublish, app.spec, t])

  const previewDoc = diffDoc
    ? dryRun.preview.find(
        (doc) => doc.kind === diffDoc.kind && doc.name === diffDoc.name
      )
    : undefined

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>
              {firstPublish
                ? t('applications.deploy.firstPublishTitle', { name: app.name })
                : copyMode
                  ? t('applications.deploy.copyCreateTitle', {
                      source: createCopyFrom,
                    })
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

          <div className="space-y-4">
            {isCreateMode && (
              <div>
                <Label>{t('applications.deploy.targetCluster')}</Label>
                <Select
                  value={targetCluster ?? ''}
                  onValueChange={setTargetCluster}
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
                  {copyMode
                    ? t('applications.deploy.copyCreateHint', {
                        source: createCopyFrom,
                      })
                    : t('applications.deploy.targetClusterHint')}
                </p>
              </div>
            )}

            <div>
              <Label>{t('applications.deploy.image')}</Label>
              <Input
                value={image}
                onChange={(event) => setImage(event.target.value)}
                className="mt-1.5 font-mono text-sm"
                disabled={!activeCluster || (!isCreateMode && !config)}
              />
              <p className="mt-1 text-xs text-muted-foreground">
                {t('applications.deploy.imageHint')}
              </p>
            </div>

            <div>
              <Label>{t('applications.deploy.pullCredential')}</Label>
              <Select
                value={pullCredential}
                onValueChange={changePullCredential}
                disabled={!activeCluster}
              >
                <SelectTrigger className="mt-1.5 w-64">
                  <SelectValue
                    placeholder={t(
                      'applications.deploy.pullCredentialPlaceholder'
                    )}
                  />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={pullCredentialNone}>
                    {t('applications.deploy.pullCredentialNone')}
                  </SelectItem>
                  {credentialNames.map((name) => (
                    <SelectItem key={name} value={name}>
                      {name}
                    </SelectItem>
                  ))}
                  {credentialEcho && (
                    <SelectItem value={credentialEcho}>
                      {credentialEcho}
                    </SelectItem>
                  )}
                </SelectContent>
              </Select>
              <p className="mt-1 text-xs text-muted-foreground">
                {t('applications.deploy.pullCredentialHint')}
              </p>
            </div>

            <div>
              <Label>{t('applications.deploy.otherConfig')}</Label>
              <div className="mt-1.5 flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2.5">
                <span className="text-xs text-muted-foreground">{summary}</span>
                {!isCreateMode && activeCluster && config && (
                  <ApplicationStatusChip
                    status={statusReport?.status ?? config.statusSummary}
                  />
                )}
                {pending && (
                  <span className="inline-flex items-center gap-1 rounded-full border border-amber-200 bg-amber-50 px-2 py-0.5 text-xs font-medium text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-400">
                    <span className="h-1.5 w-1.5 rounded-full bg-amber-500" />
                    {t('applications.deploy.pendingEdits')}
                  </span>
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
                ) : isCreateMode && !targetCluster ? (
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
                    {dryRun.preview.map((doc) => (
                      <div key={`${doc.kind}/${doc.name}`}>
                        <button
                          type="button"
                          title={t('applications.deploy.viewDocDiff')}
                          className="hover:underline"
                          onClick={() =>
                            setDiffDoc({ kind: doc.kind, name: doc.name })
                          }
                        >
                          <span className="text-sky-300">create</span>{' '}
                          {doc.kind}/{doc.name}
                        </button>
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
                    {/* Only documents with an actual content change are
                        listed: re-applied-but-identical ones would read as
                        changes that do not exist. */}
                    {dryRun.preview.map((doc) => (
                      <div key={`${doc.kind}/${doc.name}`}>
                        <button
                          type="button"
                          title={t('applications.deploy.viewDocDiff')}
                          className="hover:underline"
                          onClick={() =>
                            setDiffDoc({ kind: doc.kind, name: doc.name })
                          }
                        >
                          <span
                            className={
                              doc.action === 'create'
                                ? 'text-sky-300'
                                : 'text-emerald-300'
                            }
                          >
                            {doc.action}
                          </span>{' '}
                          {doc.kind}/{doc.name}
                        </button>
                      </div>
                    ))}
                    {imageChanged && (
                      <div>
                        <span className="text-emerald-300">update</span> image:{' '}
                        {originalImage || '—'} → {image.trim()}
                      </div>
                    )}
                    {dryRun.creates.length +
                      dryRun.updates.length -
                      dryRun.preview.length >
                      0 && (
                      <div className="text-zinc-500">
                        {t('applications.deploy.unchangedDocs', {
                          count:
                            dryRun.creates.length +
                            dryRun.updates.length -
                            dryRun.preview.length,
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
                    {isCreateMode
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

            {!isCreateMode && config?.lastDeployedAt && (
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

          <DialogFooter>
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {t('common.actions.cancel')}
            </Button>
            {!isCreateMode && (
              <Button
                variant="outline"
                disabled={!canSave}
                title={t('applications.deploy.saveOnlyHint')}
                onClick={() => void handleSaveOnly()}
              >
                {t('applications.deploy.saveOnly')}
              </Button>
            )}
            <Button disabled={!canConfirm} onClick={() => void handleDeploy()}>
              {copyMode
                ? t('applications.deploy.confirmCopyPublish')
                : firstPublish
                  ? t('applications.deploy.confirmFirstPublish')
                  : t('applications.deploy.confirmPublish')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      {previewDoc && (
        <YamlDiffViewer
          open={true}
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setDiffDoc(null)
          }}
          original={previewDoc.old ? yaml.dump(previewDoc.old) : ''}
          modified={previewDoc.new ? yaml.dump(previewDoc.new) : ''}
          title={`${previewDoc.kind}/${previewDoc.name} · ${t('applications.deploy.diffDocTitle')}`}
          hideUnchanged={true}
          height={560}
        />
      )}
    </>
  )
}
