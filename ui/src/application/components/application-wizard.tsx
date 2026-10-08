import { useEffect, useMemo, useState } from 'react'
import {
  createApplication,
  previewApplicationRender,
  updateApplication,
  useApplicationConfig,
} from '@/application/api'
import type {
  Application,
  ApplicationConfig,
  ApplicationConfigFile,
  ApplicationManifest,
  ApplicationPendingChange,
  ApplicationPortSpec,
  ApplicationSpec,
} from '@/application/types'
import { useSpaces } from '@/platform/space-context'
import { useQueryClient } from '@tanstack/react-query'
import * as yaml from 'js-yaml'
import { Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { translateError } from '@/lib/utils'
import { useCluster } from '@/hooks/use-cluster'
import { Badge } from '@/components/ui/badge'
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
import { EnvironmentEditor } from '@/components/editors/environment-editor'
import { ImageEditor } from '@/components/editors/image-editor'
import { SimpleYamlEditor } from '@/components/simple-yaml-editor'

import { ConfigFilesEditor } from './config-files-editor'
import { PortsEditor } from './ports-editor'
import { ResourceEditor } from './resource-editor'
import { CreateSpaceDialog } from './space-picker'

const STEP_COUNT = 6
const dns1123LabelPattern = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

export type ApplicationWizardMode = 'create' | 'draft' | 'cluster'

interface ApplicationWizardProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  mode: ApplicationWizardMode
  app?: Application
  cluster?: string
  /** Called after the app definition was saved (create/draft) */
  onSaved?: (app: Application) => void
  /** cluster mode: hands the pending config changes to the deploy dialog;
   *  nothing is saved here — save and publish happen on [确认发布] together. */
  onGoPublish?: (pending: ApplicationPendingChange) => void
}

interface WizardState {
  name: string
  description: string
  workloadType: string
  image: string
  replicas: number
  container: {
    name: string
    image: string
    env: Array<{ name: string; value?: string }>
    resources?: {
      requests?: { cpu?: string; memory?: string }
      limits?: { cpu?: string; memory?: string }
    }
  }
  mountPoints: string[]
  configFiles: ApplicationConfigFile[]
  ports: ApplicationPortSpec[]
  ingressClass: string
  extraYaml: string
}

function buildState(
  mode: ApplicationWizardMode,
  app: Application | undefined,
  config: ApplicationConfig | undefined
): WizardState {
  const spec: ApplicationSpec | undefined =
    mode === 'cluster' ? config?.spec : app?.spec
  const workload = spec?.workload
  return {
    name: mode === 'create' ? (app?.name ?? '') : (app?.name ?? ''),
    description: app?.description ?? '',
    workloadType: workload?.type || 'deployment',
    image: workload?.image || '',
    replicas: workload?.replicas ?? 1,
    container: {
      name: 'app',
      image: workload?.image || '',
      env: (workload?.env ?? []).map((env) => ({
        name: env.name,
        value: env.value,
      })),
      resources: {
        requests: workload?.requests,
        limits: workload?.limits,
      },
    },
    mountPoints: spec?.mountPoints ?? [],
    configFiles: spec?.configFiles ?? [],
    ports: spec?.ports ?? [],
    ingressClass: spec?.ingressClass ?? '',
    extraYaml: spec?.extraYaml ?? '',
  }
}

function buildSpec(state: WizardState): ApplicationSpec {
  return {
    workload: {
      type: state.workloadType,
      image: state.image.trim(),
      replicas: state.replicas,
      env: state.container.env.filter((env) => env.name.trim() !== ''),
      requests: state.container.resources?.requests,
      limits: state.container.resources?.limits,
    },
    mountPoints: state.mountPoints,
    configFiles: state.configFiles,
    ports: state.ports.filter((port) => port.port > 0),
    ingressClass: state.ingressClass || undefined,
    extraYaml: state.extraYaml || undefined,
  }
}

// Creation / editing wizard (design §11): six steps. Creation finishes with a
// target-cluster pick and one chain — create app + render config + first
// publish; the draft mode only saves the definition; cluster mode never saves
// on its own — [去发布] hands the edited {spec}/{manifests} plus expectedRev
// to the deploy dialog, where 确认发布 persists the revision and applies it
// in one request.
export function ApplicationWizard({
  open,
  onOpenChange,
  mode,
  app,
  cluster,
  onSaved,
  onGoPublish,
}: ApplicationWizardProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { spaces, currentSpace, isLoading: spacesLoading } = useSpaces()
  const { clusters } = useCluster()
  // Create mode targets a space picked from the existing list — creation is
  // blocked outright when no space exists (a space is a labeled namespace,
  // there is no 'default' fallback).
  const [space, setSpace] = useState('')
  const [createSpaceOpen, setCreateSpaceOpen] = useState(false)
  // Create mode's publish target: required, so finishing always deploys.
  const [targetCluster, setTargetCluster] = useState('')
  const effectiveNamespace = mode === 'create' ? space : (app?.namespace ?? '')
  const noSpaces = mode === 'create' && !spacesLoading && spaces.length === 0

  // Cluster mode pulls the config itself: form prefill from the extracted
  // spec, YAML mode from the manifests, saves under the latestRev lock.
  const { data: config } = useApplicationConfig(
    mode === 'cluster' && open ? app?.name : undefined,
    mode === 'cluster' && open ? cluster : undefined
  )

  const [step, setStep] = useState(1)
  const [state, setState] = useState<WizardState>(() =>
    buildState(mode, app, config)
  )
  const [yamlMode, setYamlMode] = useState(false)
  const [yamlContent, setYamlContent] = useState('')
  const [previewYaml, setPreviewYaml] = useState('')
  const [previewLoading, setPreviewLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    // Rebuild the draft when the dialog opens for a target, or when the
    // cluster config loads/advances a revision (keyed by latestRev so a
    // background refetch never clobbers in-progress edits).
    if (!open) return
    if (mode === 'cluster' && !config) return
    setStep(1)
    setYamlMode(false)
    setYamlContent('')
    setPreviewYaml('')
    setError('')
    if (mode === 'create') {
      setSpace(currentSpace ?? '')
      setTargetCluster('')
    }
    setState(buildState(mode, app, config))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, mode, app?.name, config?.latestRev, cluster])

  // GET /clusters only returns RBAC-visible clusters; offline ones carry
  // `error` (same filter as the deploy dialog's dropdown).
  const availableClusters = useMemo(
    () => clusters.filter((candidate) => !candidate.error),
    [clusters]
  )

  const patch = (updates: Partial<WizardState>) =>
    setState((prev) => ({ ...prev, ...updates }))

  const spec = useMemo(() => buildSpec(state), [state])

  // A create-mode draft without a valid name is rejected on save; the preview
  // skips it too so it never renders a misleading placeholder-name manifest.
  const nameInvalid =
    mode === 'create' &&
    (!dns1123LabelPattern.test(state.name) || state.name.length > 63)

  const loadPreview = async () => {
    if (nameInvalid || (mode === 'create' && !effectiveNamespace)) {
      setPreviewYaml('')
      setPreviewLoading(false)
      return
    }
    setPreviewLoading(true)
    try {
      const result = await previewApplicationRender({
        name: state.name || 'app',
        namespace: effectiveNamespace,
        spec,
      })
      setPreviewYaml(result.yaml)
    } catch (err) {
      setPreviewYaml('')
      toast.error(translateError(err, t))
    } finally {
      setPreviewLoading(false)
    }
  }

  useEffect(() => {
    if (open && step === STEP_COUNT) {
      void loadPreview()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, step])

  const toggleYamlMode = (on: boolean) => {
    if (on) {
      if (mode === 'cluster' && config?.manifests?.length) {
        setYamlContent(yaml.dump(config.manifests))
      } else if (previewYaml) {
        setYamlContent(previewYaml)
      }
    }
    setYamlMode(on)
  }

  const limitsMissing = !(
    state.container.resources?.limits?.cpu?.trim() &&
    state.container.resources?.limits?.memory?.trim()
  )

  const validate = (): string => {
    if (mode === 'create') {
      if (!effectiveNamespace) {
        return t('applications.wizard.spaceRequired')
      }
      if (!dns1123LabelPattern.test(state.name) || state.name.length > 63) {
        return t('applications.wizard.invalidName')
      }
      if (!targetCluster) {
        return t('applications.wizard.clusterRequired')
      }
    }
    if (!state.image.trim()) {
      return t('applications.wizard.imageRequired')
    }
    if (limitsMissing) {
      return t('applications.wizard.limitsRequired')
    }
    for (const port of spec.ports ?? []) {
      if (port.port < 1 || port.port > 65535) {
        return t('applications.wizard.invalidPort', { port: port.port })
      }
    }
    return ''
  }

  // Per-step gate for the Next button: required fields of the current step
  // must pass before the user can move on (config-files and the rest are
  // optional by design — design §9/§8).
  const stepError = (() => {
    if (step === 1) {
      if (mode === 'create' && !effectiveNamespace) {
        return t('applications.wizard.spaceRequired')
      }
      if (nameInvalid) return t('applications.wizard.invalidName')
      if (!state.image.trim()) return t('applications.wizard.imageRequired')
    }
    if (step === 2 && limitsMissing) {
      return t('applications.wizard.limitsRequired')
    }
    if (step === 4) {
      // state.ports, not spec.ports — buildSpec drops rows below the valid
      // range, which would silently pass the gate.
      for (const port of state.ports ?? []) {
        if (port.port < 1 || port.port > 65535) {
          return t('applications.wizard.invalidPort', { port: port.port })
        }
      }
    }
    if (step === 5 && mode === 'create' && !targetCluster) {
      return t('applications.wizard.clusterRequired')
    }
    return ''
  })()

  const finishLabel =
    mode === 'cluster'
      ? undefined
      : mode === 'draft'
        ? t('applications.wizard.createApp')
        : t('applications.wizard.createPublish')

  // Cluster mode hands the edits to the deploy dialog without saving; a
  // cancelled publish discards them, so no revision is created behind the
  // user's back.
  const goPublish = () => {
    const validationError = validate()
    if (validationError) {
      setError(validationError)
      return
    }
    if (!app || !cluster) return
    // Close first: the parent clears openDialog on close, which would
    // otherwise clobber the 'deploy' state onGoPublish is about to set.
    onOpenChange(false)
    if (yamlMode) {
      let manifests: ApplicationManifest[]
      try {
        manifests = yaml.load(yamlContent) as ApplicationManifest[]
      } catch {
        setError(t('applications.wizard.invalidYaml'))
        return
      }
      onGoPublish?.({ manifests, expectedRev: config?.latestRev ?? 0 })
    } else {
      onGoPublish?.({ spec, expectedRev: config?.latestRev ?? 0 })
    }
  }

  const handleFinish = async () => {
    const validationError = validate()
    if (validationError) {
      setError(validationError)
      return
    }
    setSaving(true)
    setError('')
    try {
      if (mode === 'draft' && app) {
        const updated = await updateApplication(app.name, {
          description: state.description,
          spec,
        })
        await queryClient.invalidateQueries({ queryKey: ['applications'] })
        toast.success(t('applications.wizard.appSaved'))
        onOpenChange(false)
        onSaved?.(updated)
      } else {
        const created = await createApplication({
          name: state.name,
          namespace: effectiveNamespace,
          description: state.description,
          spec,
          cluster: targetCluster,
        })
        await queryClient.invalidateQueries({ queryKey: ['applications'] })
        // Same result handling as the deploy dialog: partial document
        // failures still count as a created app, so warn instead of error.
        const deploy = created.deploy
        if (deploy && !deploy.success) {
          toast.warning(
            t('applications.deploy.partialFailure', {
              failed: deploy.results.filter((docResult) => !docResult.success)
                .length,
            })
          )
        } else if (deploy) {
          toast.success(
            t('applications.wizard.appCreatedPublished', {
              name: created.name,
              cluster: targetCluster,
            })
          )
        } else {
          toast.success(
            t('applications.wizard.appCreated', { name: created.name })
          )
        }
        onOpenChange(false)
        onSaved?.(created)
      }
    } catch (err) {
      setError(translateError(err, t))
    } finally {
      setSaving(false)
    }
  }

  const stepLabels = [
    t('applications.wizard.stepBasic'),
    t('applications.wizard.stepRuntime'),
    t('applications.wizard.stepConfigFiles'),
    t('applications.wizard.stepNetwork'),
    t('applications.wizard.stepTargetCluster'),
    t('applications.wizard.stepPreview'),
  ]

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] sm:max-w-none flex-col gap-0 overflow-hidden p-0">
        <DialogHeader className="border-b px-6 py-4">
          <DialogTitle className="flex items-center gap-2 text-base">
            {mode === 'create'
              ? t('applications.wizard.createTitle')
              : mode === 'draft'
                ? t('applications.wizard.editDraftTitle', { name: app?.name })
                : t('applications.wizard.editClusterTitle', {
                    name: app?.name,
                    cluster,
                  })}
            <Badge variant="outline" className="font-mono">
              {effectiveNamespace}
            </Badge>
          </DialogTitle>
          <DialogDescription className="sr-only">
            {t('applications.wizard.createTitle')}
          </DialogDescription>
        </DialogHeader>

        {!noSpaces && (
          <div className="flex items-center gap-2 overflow-x-auto border-b px-6 py-3 text-xs">
            {stepLabels.map((label, index) => {
              const number = index + 1
              return (
                <div key={label} className="flex items-center gap-2">
                  {index > 0 && <span className="h-px w-5 bg-border" />}
                  <button
                    type="button"
                    className="flex items-center gap-2"
                    onClick={() => setStep(number)}
                  >
                    <span
                      className={
                        number === step
                          ? 'flex h-6 w-6 shrink-0 items-center justify-center rounded-full border-2 border-primary bg-primary text-[10px] font-semibold text-primary-foreground'
                          : number < step
                            ? 'flex h-6 w-6 shrink-0 items-center justify-center rounded-full border-2 border-green-500 bg-green-50 text-[10px] font-semibold text-green-600 dark:bg-green-950'
                            : 'flex h-6 w-6 shrink-0 items-center justify-center rounded-full border-2 border-border bg-background text-[10px] font-semibold text-muted-foreground'
                      }
                    >
                      {number}
                    </span>
                    <span
                      className={
                        number === step
                          ? 'whitespace-nowrap font-medium text-foreground'
                          : 'whitespace-nowrap text-muted-foreground'
                      }
                    >
                      {label}
                    </span>
                  </button>
                </div>
              )
            })}
          </div>
        )}

        <div className="min-h-80 flex-1 overflow-y-auto px-6 py-4">
          {noSpaces ? (
            <div className="flex flex-col items-center justify-center gap-2 py-16 text-center">
              <div className="text-sm font-medium">
                {t('applications.wizard.noSpacesTitle')}
              </div>
              <p className="max-w-md text-xs text-muted-foreground">
                {t('applications.wizard.noSpacesHint')}
              </p>
              <Button
                type="button"
                size="sm"
                className="mt-2"
                onClick={() => setCreateSpaceOpen(true)}
              >
                <Plus className="mr-1 h-3.5 w-3.5" />
                {t('sidebar.spaces.create')}
              </Button>
            </div>
          ) : mode === 'cluster' && !config ? (
            <div className="py-16 text-center text-sm text-muted-foreground">
              {t('common.messages.loading')}
            </div>
          ) : (
            <>
              {step === 1 && (
                <div className="space-y-4">
                  <div>
                    <Label>{t('applications.wizard.appName')}</Label>
                    {mode === 'create' ? (
                      <div className="flex">
                        <Select value={space} onValueChange={setSpace}>
                          <SelectTrigger className="w-44 shrink-0 rounded-r-none border-r-0 font-mono">
                            <SelectValue
                              placeholder={t(
                                'applications.wizard.spacePlaceholder'
                              )}
                            />
                          </SelectTrigger>
                          <SelectContent>
                            {spaces.map((candidate) => (
                              <SelectItem
                                key={candidate}
                                value={candidate}
                                className="font-mono"
                              >
                                {candidate}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                        <span className="flex h-9 items-center border-y bg-muted px-1 text-sm text-muted-foreground">
                          /
                        </span>
                        <Input
                          value={state.name}
                          onChange={(event) =>
                            patch({ name: event.target.value })
                          }
                          placeholder="my-app"
                          className="rounded-l-none font-mono"
                        />
                      </div>
                    ) : (
                      <div className="flex h-9 items-center rounded-md border bg-muted px-3 font-mono text-sm text-muted-foreground">
                        {effectiveNamespace} / {app?.name}
                      </div>
                    )}
                    <p className="mt-1 text-xs text-muted-foreground">
                      {t('applications.wizard.appNameHint')}
                    </p>
                  </div>
                  <div>
                    <Label>{t('applications.wizard.description')}</Label>
                    <Input
                      value={state.description}
                      onChange={(event) =>
                        patch({ description: event.target.value })
                      }
                      className="mt-1.5"
                    />
                  </div>
                  <div>
                    <Label>{t('applications.wizard.workloadType')}</Label>
                    <Select
                      value={state.workloadType}
                      onValueChange={(value) => patch({ workloadType: value })}
                      disabled={
                        mode === 'cluster' &&
                        !!config &&
                        config.statusSummary !== 'undeployed'
                      }
                    >
                      <SelectTrigger className="mt-1.5 w-56">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="deployment">Deployment</SelectItem>
                        <SelectItem value="statefulset">StatefulSet</SelectItem>
                      </SelectContent>
                    </Select>
                    {mode === 'cluster' &&
                      !!config &&
                      config.statusSummary !== 'undeployed' && (
                        <p className="mt-1 text-xs text-muted-foreground">
                          {t('applications.wizard.workloadTypeLocked')}
                        </p>
                      )}
                  </div>
                  <div className="grid gap-4 md:grid-cols-[1fr_8rem]">
                    <div>
                      <Label>{t('applications.wizard.image')}</Label>
                      <div className="mt-1.5">
                        <ImageEditor
                          container={
                            { name: 'app', image: state.image } as never
                          }
                          onUpdate={(updates) => {
                            if (updates.image !== undefined) {
                              patch({
                                image: updates.image,
                                container: {
                                  ...state.container,
                                  image: updates.image,
                                },
                              })
                            }
                          }}
                        />
                      </div>
                    </div>
                    <div>
                      <Label>{t('applications.wizard.replicas')}</Label>
                      <Input
                        type="number"
                        min={0}
                        value={state.replicas}
                        onChange={(event) =>
                          patch({ replicas: Number(event.target.value) || 0 })
                        }
                        className="mt-1.5"
                      />
                    </div>
                  </div>
                </div>
              )}

              {step === 2 && (
                <div className="space-y-6">
                  <EnvironmentEditor
                    container={state.container as never}
                    namespace={effectiveNamespace}
                    onUpdate={(updates) =>
                      patch({
                        container: { ...state.container, ...updates },
                      })
                    }
                  />
                  <ResourceEditor
                    container={state.container as never}
                    onUpdate={(updates) =>
                      patch({
                        container: {
                          ...state.container,
                          resources: {
                            ...state.container.resources,
                            ...updates.resources,
                          },
                        },
                      })
                    }
                  />
                </div>
              )}

              {step === 3 && (
                <ConfigFilesEditor
                  appName={state.name || app?.name || 'app'}
                  mountPoints={state.mountPoints}
                  configFiles={state.configFiles}
                  onChange={(mountPoints, configFiles) =>
                    patch({ mountPoints, configFiles })
                  }
                />
              )}

              {step === 4 && (
                <PortsEditor
                  ports={state.ports}
                  ingressClass={state.ingressClass}
                  onChange={(ports, ingressClass) =>
                    patch({ ports, ingressClass })
                  }
                />
              )}

              {step === 5 && (
                <div className="space-y-4">
                  {mode === 'create' ? (
                    <div>
                      <Label>{t('applications.wizard.targetCluster')}</Label>
                      <Select
                        value={targetCluster}
                        onValueChange={setTargetCluster}
                      >
                        <SelectTrigger className="mt-1.5 w-72">
                          <SelectValue
                            placeholder={t('applications.deploy.pickCluster')}
                          />
                        </SelectTrigger>
                        <SelectContent>
                          {availableClusters.map((candidate) => (
                            <SelectItem
                              key={candidate.name}
                              value={candidate.name}
                            >
                              {candidate.name} · {candidate.version ?? ''}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                      <p className="mt-1 text-xs text-muted-foreground">
                        {t('applications.wizard.targetClusterHint')}
                      </p>
                    </div>
                  ) : (
                    <div>
                      <Label>
                        {t('applications.wizard.targetClusterReadOnly')}
                      </Label>
                      {mode === 'cluster' && cluster ? (
                        <div className="flex items-center gap-3 rounded-lg border bg-muted/40 px-4 py-3">
                          <span className="text-sm font-medium">{cluster}</span>
                          <Badge variant="outline" className="font-mono">
                            {effectiveNamespace}
                          </Badge>
                        </div>
                      ) : (
                        <div className="rounded-lg border border-dashed px-4 py-6 text-center">
                          <div className="text-sm text-muted-foreground">
                            {t('applications.wizard.draftNoCluster')}
                          </div>
                          <div className="mt-1 text-xs text-muted-foreground/70">
                            {t('applications.wizard.draftNoClusterHint')}
                          </div>
                        </div>
                      )}
                    </div>
                  )}
                  <div>
                    <Label>{t('sidebar.spaces.label')}</Label>
                    <div className="mt-1.5 flex h-9 items-center">
                      <Badge variant="outline" className="font-mono">
                        {effectiveNamespace}
                      </Badge>
                    </div>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {t('applications.wizard.spaceReadOnlyHint')}
                    </p>
                  </div>
                </div>
              )}

              {step === STEP_COUNT && (
                <div className="space-y-3">
                  {nameInvalid && (
                    <div className="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2.5 text-xs text-destructive">
                      <div>{t('applications.wizard.invalidName')}</div>
                      {!state.name.trim() && (
                        <div className="mt-1 opacity-80">
                          {t('applications.wizard.previewPlaceholderHint')}
                        </div>
                      )}
                    </div>
                  )}
                  <div className="flex items-center justify-between">
                    <div className="text-sm font-medium">
                      {yamlMode
                        ? t('applications.wizard.yamlMode')
                        : t('applications.wizard.previewMode')}
                    </div>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      onClick={() => toggleYamlMode(!yamlMode)}
                    >
                      {yamlMode
                        ? t('applications.wizard.backToPreview')
                        : t('applications.wizard.switchToYaml')}
                    </Button>
                  </div>
                  {yamlMode ? (
                    <>
                      <SimpleYamlEditor
                        value={yamlContent}
                        onChange={(value) => setYamlContent(value ?? '')}
                        disabled={mode !== 'cluster'}
                        height="360px"
                      />
                      {mode !== 'cluster' && (
                        <p className="text-xs text-muted-foreground">
                          {t('applications.wizard.yamlReadOnlyHint')}
                        </p>
                      )}
                      {mode === 'cluster' && (
                        <p className="text-xs text-muted-foreground">
                          {t('applications.wizard.yamlEditableHint')}
                        </p>
                      )}
                    </>
                  ) : (
                    <>
                      <pre className="max-h-72 overflow-auto rounded-lg bg-zinc-950 p-4 font-mono text-xs leading-5 text-zinc-300">
                        {previewLoading
                          ? t('common.messages.loading')
                          : previewYaml ||
                            t('applications.wizard.previewEmpty')}
                      </pre>
                      <div>
                        <Label>{t('applications.wizard.extraYaml')}</Label>
                        <textarea
                          value={state.extraYaml}
                          onChange={(event) =>
                            patch({ extraYaml: event.target.value })
                          }
                          placeholder={t(
                            'applications.wizard.extraYamlPlaceholder'
                          )}
                          className="mt-1.5 h-40 w-full rounded-md border bg-background p-3 font-mono text-xs outline-none placeholder:text-muted-foreground"
                        />
                        <p className="mt-1 text-xs text-muted-foreground">
                          {t('applications.wizard.extraYamlHint')}
                        </p>
                      </div>
                    </>
                  )}
                </div>
              )}

              {error && (
                <p className="mt-3 text-xs text-destructive">{error}</p>
              )}
            </>
          )}
        </div>

        <DialogFooter className="flex-row items-center justify-between border-t px-6 py-4">
          <div className="flex min-w-0 items-center gap-3">
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
            >
              {t('common.actions.cancel')}
            </Button>
            {stepError && (
              <p className="truncate text-xs text-destructive">{stepError}</p>
            )}
          </div>
          {!noSpaces && (
            <div className="flex gap-2">
              {step > 1 && (
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => setStep(step - 1)}
                >
                  {t('applications.wizard.previous')}
                </Button>
              )}
              {step < STEP_COUNT && (
                <Button
                  type="button"
                  disabled={!!stepError}
                  onClick={() => setStep(step + 1)}
                >
                  {t('applications.wizard.next')}
                </Button>
              )}
              {step === STEP_COUNT && mode === 'cluster' && (
                <Button type="button" onClick={goPublish}>
                  {t('applications.wizard.goPublish')}
                </Button>
              )}
              {step === STEP_COUNT && mode !== 'cluster' && (
                <Button
                  type="button"
                  disabled={saving}
                  onClick={() => void handleFinish()}
                >
                  {finishLabel}
                </Button>
              )}
            </div>
          )}
        </DialogFooter>
      </DialogContent>
      <CreateSpaceDialog
        open={createSpaceOpen}
        onOpenChange={setCreateSpaceOpen}
      />
    </Dialog>
  )
}
