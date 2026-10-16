import { useMemo, useRef, useState } from 'react'
import {
  ApplicationApiError,
  createApplication,
  deployApplicationConfig,
  useSpaceQuotas,
} from '@/portal/application/api'
import type { ApplicationManifest } from '@/portal/application/types'
import { useSpaces } from '@/portal/space-context'
import { useQueryClient } from '@tanstack/react-query'
import * as yaml from 'js-yaml'
import { AlertTriangle, Check, Upload, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { translateError } from '@/lib/utils'
import { useCluster } from '@/hooks/use-cluster'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
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
import { SimpleYamlEditor } from '@/components/simple-yaml-editor'

const dns1123LabelPattern = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

// Server-side redaction marker (api/redact.go): exported files carry "***"
// secret values. Import saves them as-is (a warning per application); the
// existing pre-publish validation catches them when publishing later.
const redactionMarker = '***'

// The workload document identifying an application inside a multi-document
// stream: the first hit provides the detected application name.
const workloadKinds = ['Deployment', 'StatefulSet', 'DaemonSet']

// Stable id of the paste editor's application row (file rows get generated ids).
const pasteAppId = 'paste'

function containsMaskedValue(value: unknown): boolean {
  if (value === redactionMarker) return true
  if (Array.isArray(value)) return value.some(containsMaskedValue)
  if (value && typeof value === 'object') {
    return Object.values(value).some(containsMaskedValue)
  }
  return false
}

// One recognized import candidate: the parsed multi-document stream of one
// source (file or paste) plus the workload name identifying the application.
interface ImportApp {
  id: string
  source: string
  docs: ApplicationManifest[]
  detectedName: string
  masked: boolean
}

// A file that could not be turned into an application; kept visible so it can
// be removed (or the file fixed and re-picked).
interface FailedFile {
  id: string
  fileName: string
  reason: string
}

interface ImportResult {
  name: string
  ok: boolean
  error?: string
}

interface ImportDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

// App import (design §11): pick or paste exported multi-document YAML, one
// application per source identified by its workload document. Import only
// saves — a draft-only app is created and the manifests land as the target
// cluster's pending config ({saveOnly:true}); publishing happens later from
// the application detail page.
export function ImportDialog({ open, onOpenChange }: ImportDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open ? <ImportDialogContent onOpenChange={onOpenChange} /> : null}
    </Dialog>
  )
}

type ParseError = { key: string; index?: number }

function parseDocs(raw: string): {
  docs: ApplicationManifest[] | null
  error?: ParseError
} {
  let loaded: unknown[]
  try {
    loaded = yaml.loadAll(raw)
  } catch {
    return { docs: null, error: { key: 'invalidYaml' } }
  }
  const docs = loaded.filter((doc) => doc != null) as ApplicationManifest[]
  if (docs.length === 0) {
    return { docs: null, error: { key: 'noDocs' } }
  }
  for (let i = 0; i < docs.length; i++) {
    const doc = docs[i]
    if (
      typeof doc !== 'object' ||
      Array.isArray(doc) ||
      !doc.apiVersion ||
      !doc.kind ||
      !doc.metadata?.name
    ) {
      return { docs: null, error: { key: 'invalidDoc', index: i } }
    }
  }
  return { docs }
}

function detectAppName(docs: ApplicationManifest[]): string | null {
  for (const kind of workloadKinds) {
    const workload = docs.find(
      (doc) => doc.kind === kind && !!doc.metadata?.name
    )
    if (workload) return workload.metadata!.name
  }
  return null
}

function ImportDialogContent({
  onOpenChange,
}: Omit<ImportDialogProps, 'open'>) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { clusters } = useCluster()
  const { currentSpace, spaceClusters } = useSpaces()

  const [phase, setPhase] = useState<'select' | 'target'>('select')
  const [files, setFiles] = useState<ImportApp[]>([])
  const [failedFiles, setFailedFiles] = useState<FailedFile[]>([])
  const [content, setContent] = useState('')
  // Editable application name per row, keyed by the row id; absent = the
  // detected name.
  const [names, setNames] = useState<Record<string, string>>({})
  // Row selection, keyed by the row id; absent = checked.
  const [checked, setChecked] = useState<Record<string, boolean>>({})
  const [targetCluster, setTargetCluster] = useState('')
  const [importing, setImporting] = useState(false)
  const [results, setResults] = useState<ImportResult[]>([])
  const nextIdRef = useRef(0)

  const space = currentSpace ?? ''

  // Publish targets follow the wizard's create-mode cluster pick: online
  // clusters the space was adopted on (quota configured AND the labeled
  // namespace present).
  const { data: spaceQuotaRows, isError: spaceQuotasFailed } = useSpaceQuotas(
    space || undefined
  )
  const availableClusters = useMemo(() => {
    const usable = clusters.filter((candidate) => !candidate.error)
    if (!space || spaceQuotasFailed) {
      return usable
    }
    const nsSet = new Set(spaceClusters[space] ?? [])
    const adopted = new Set(
      (spaceQuotaRows?.quotas ?? [])
        .filter(
          (row) =>
            !row.unconfigured &&
            !row.unauthorized &&
            !row.error &&
            nsSet.has(row.cluster)
        )
        .map((row) => row.cluster)
    )
    return usable.filter((candidate) => adopted.has(candidate.name))
  }, [clusters, space, spaceClusters, spaceQuotaRows, spaceQuotasFailed])

  // The paste editor's application, derived live from the editor content.
  const pasteApp = useMemo<{ app?: ImportApp; error?: ParseError }>(() => {
    if (!content.trim()) return {}
    const parsed = parseDocs(content)
    if (parsed.error || !parsed.docs) return { error: parsed.error }
    const detectedName = detectAppName(parsed.docs)
    if (!detectedName) return { error: { key: 'noWorkload' } }
    return {
      app: {
        id: pasteAppId,
        source: t('applications.import.pasteSource'),
        docs: parsed.docs,
        detectedName,
        masked: containsMaskedValue(parsed.docs),
      },
    }
  }, [content, t])

  const apps = useMemo(
    () => (pasteApp.app ? [...files, pasteApp.app] : files),
    [files, pasteApp]
  )

  const nameOf = (app: ImportApp) => names[app.id] ?? app.detectedName
  const nameInvalid = (name: string) =>
    !dns1123LabelPattern.test(name) || name.length > 63
  const isChecked = (app: ImportApp) => checked[app.id] ?? true

  const checkedApps = apps.filter(isChecked)
  const canNext =
    checkedApps.length > 0 &&
    checkedApps.every((app) => !nameInvalid(nameOf(app)))

  const handleFilesSelect = async (
    event: React.ChangeEvent<HTMLInputElement>
  ) => {
    // Capture the File objects, then reset so picking the same file again
    // re-fires onChange.
    const picked = Array.from(event.target.files ?? [])
    event.target.value = ''
    if (picked.length === 0) return
    const nextApps: ImportApp[] = []
    const nextFailed: FailedFile[] = []
    for (const file of picked) {
      const fail = (reason: string) =>
        nextFailed.push({
          id: `failed-${nextIdRef.current++}`,
          fileName: file.name,
          reason,
        })
      let raw: string
      try {
        raw = await file.text()
      } catch {
        fail(t('applications.import.fileReadError'))
        continue
      }
      const parsed = parseDocs(raw)
      if (parsed.error || !parsed.docs) {
        fail(
          parsed.error?.key === 'invalidDoc'
            ? t('applications.import.invalidDoc', {
                index: (parsed.error.index ?? 0) + 1,
              })
            : t(`applications.import.${parsed.error?.key ?? 'invalidYaml'}`)
        )
        continue
      }
      const detectedName = detectAppName(parsed.docs)
      if (!detectedName) {
        fail(t('applications.import.noWorkload'))
        continue
      }
      nextApps.push({
        id: `file-${nextIdRef.current++}`,
        source: file.name,
        docs: parsed.docs,
        detectedName,
        masked: containsMaskedValue(parsed.docs),
      })
    }
    setFiles((prev) => [...prev, ...nextApps])
    setFailedFiles((prev) => [...prev, ...nextFailed])
  }

  const removeFile = (id: string) => {
    setFiles((prev) => prev.filter((app) => app.id !== id))
    setFailedFiles((prev) => prev.filter((file) => file.id !== id))
  }

  // Import = save only: per checked application, a draft-only app row and a
  // {saveOnly} deploy persisting the manifests as the cluster's pending
  // config. Applications run sequentially; one failure does not stop the
  // rest, and nothing is published here.
  const runImport = async () => {
    if (!space || !targetCluster) return
    const selected = checkedApps
    setImporting(true)
    setResults([])
    const collected: ImportResult[] = []
    for (const app of selected) {
      const name = nameOf(app)
      try {
        try {
          await createApplication({ name, namespace: space })
        } catch (err) {
          if (err instanceof ApplicationApiError && err.status === 409) {
            throw new Error(t('applications.wizard.nameExists'), {
              cause: err,
            })
          }
          throw err
        }
        // parseDocs validated metadata.name on every document; the config
        // namespace is authoritative, so every doc rides the target space.
        const docs = app.docs.map((doc) => ({
          ...doc,
          metadata: { ...doc.metadata!, namespace: space },
        }))
        await deployApplicationConfig(space, name, targetCluster, {
          manifests: docs,
          saveOnly: true,
        })
        collected.push({ name, ok: true })
      } catch (err) {
        collected.push({
          name,
          ok: false,
          error: err instanceof Error ? err.message : translateError(err, t),
        })
      }
    }
    setResults(collected)
    setImporting(false)
    await queryClient.invalidateQueries({ queryKey: ['applications'] })
  }

  const canImport =
    phase === 'target' &&
    !importing &&
    !!space &&
    !!targetCluster &&
    checkedApps.length > 0 &&
    checkedApps.every((app) => !nameInvalid(nameOf(app)))

  return (
    <DialogContent className="flex h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] sm:max-w-3xl flex-col gap-0 overflow-hidden p-0">
      <DialogHeader className="border-b px-6 py-4">
        <DialogTitle className="flex items-center gap-2 text-base">
          <Upload className="size-5" />
          {t('applications.import.title')}
        </DialogTitle>
        <DialogDescription className="sr-only">
          {t('applications.import.description')}
        </DialogDescription>
      </DialogHeader>

      <div className="flex-1 space-y-4 overflow-y-auto px-6 py-4">
        {phase === 'select' && (
          <>
            {!space && (
              <div className="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2.5 text-xs text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-400">
                {t('applications.wizard.spaceRequired')}
              </div>
            )}
            <div>
              <Label htmlFor="import-file">
                {t('applications.import.file')}
              </Label>
              <Input
                id="import-file"
                type="file"
                multiple
                accept=".yaml,.yml,text/yaml"
                onChange={(event) => void handleFilesSelect(event)}
              />
              <p className="mt-1 text-xs text-muted-foreground">
                {t('applications.import.fileHint')}
              </p>
            </div>

            {(apps.length > 0 || failedFiles.length > 0) && (
              <div className="space-y-2">
                <div className="flex items-center gap-3 px-1 text-xs font-medium text-muted-foreground">
                  <span className="w-4" />
                  <span className="w-44 shrink-0">
                    {t('applications.import.detectedName')}
                  </span>
                  <span className="flex-1">
                    {t('applications.import.appName')}
                  </span>
                </div>
                {failedFiles.map((file) => (
                  <div
                    key={file.id}
                    className="flex items-center gap-2 rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-xs"
                  >
                    <AlertTriangle className="size-4 shrink-0 text-destructive" />
                    <span className="w-44 min-w-0 shrink-0 truncate font-mono">
                      {file.fileName}
                    </span>
                    <span className="min-w-0 flex-1 truncate text-destructive">
                      {file.reason}
                    </span>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="size-6 shrink-0"
                      title={t('applications.import.remove')}
                      onClick={() => removeFile(file.id)}
                    >
                      <X className="size-3.5" />
                    </Button>
                  </div>
                ))}
                {apps.map((app) => (
                  <div
                    key={app.id}
                    className="flex items-center gap-3 rounded-lg border px-3 py-2"
                  >
                    <Checkbox
                      checked={isChecked(app)}
                      onCheckedChange={(value) =>
                        setChecked((prev) => ({
                          ...prev,
                          [app.id]: value === true,
                        }))
                      }
                    />
                    <div className="w-44 min-w-0 shrink-0">
                      <div className="truncate text-xs text-muted-foreground">
                        {app.source}
                      </div>
                      <div className="truncate font-mono text-xs">
                        {app.detectedName}
                      </div>
                    </div>
                    <Input
                      value={nameOf(app)}
                      onChange={(event) =>
                        setNames((prev) => ({
                          ...prev,
                          [app.id]: event.target.value,
                        }))
                      }
                      aria-label={t('applications.import.appName')}
                      className="h-8 min-w-0 flex-1 font-mono text-xs"
                    />
                    {nameInvalid(nameOf(app)) && (
                      <span
                        className="shrink-0 text-xs text-destructive"
                        title={t('applications.wizard.invalidName')}
                      >
                        <AlertTriangle className="size-4" />
                      </span>
                    )}
                    {app.masked && (
                      <span
                        className="shrink-0 text-amber-600 dark:text-amber-400"
                        title={t('applications.import.maskedWarning')}
                      >
                        <AlertTriangle className="size-4" />
                      </span>
                    )}
                    {app.id !== pasteAppId && (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-6 shrink-0"
                        title={t('applications.import.remove')}
                        onClick={() => removeFile(app.id)}
                      >
                        <X className="size-3.5" />
                      </Button>
                    )}
                  </div>
                ))}
              </div>
            )}

            <div>
              <Label htmlFor="import-content">
                {t('applications.import.content')}
              </Label>
              <div className="mt-1.5">
                <SimpleYamlEditor
                  value={content}
                  onChange={(value) => setContent(value ?? '')}
                  height="220px"
                />
              </div>
              <p className="mt-1 text-xs text-muted-foreground">
                {t('applications.import.contentHint')}
              </p>
              {pasteApp.error && (
                <p role="alert" className="mt-2 text-xs text-destructive">
                  {pasteApp.error.key === 'invalidDoc'
                    ? t('applications.import.invalidDoc', {
                        index: (pasteApp.error.index ?? 0) + 1,
                      })
                    : t(`applications.import.${pasteApp.error.key}`)}
                </p>
              )}
            </div>
          </>
        )}

        {phase === 'target' && (
          <>
            <div>
              <Label>{t('applications.import.space')}</Label>
              <Input
                value={space}
                readOnly
                disabled
                className="mt-1.5 font-mono"
              />
              <p className="mt-1 text-xs text-muted-foreground">
                {t('applications.import.spaceHint')}
              </p>
            </div>

            <div>
              <Label>{t('applications.deploy.targetCluster')}</Label>
              <Select
                value={targetCluster}
                onValueChange={setTargetCluster}
                disabled={importing}
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
                {t('applications.import.clusterHint')}
              </p>
            </div>

            <div className="rounded-lg bg-muted/50 px-3 py-2.5 text-xs text-muted-foreground">
              {t('applications.import.saveOnlyHint')}
            </div>

            {results.length > 0 && (
              <div>
                <Label>{t('applications.import.resultsTitle')}</Label>
                <div className="mt-1.5 space-y-1">
                  {results.map((result) => (
                    <div
                      key={result.name}
                      className="flex items-start gap-2 rounded-lg border px-3 py-2 text-xs"
                    >
                      {result.ok ? (
                        <Check className="mt-0.5 size-3.5 shrink-0 text-green-600" />
                      ) : (
                        <X className="mt-0.5 size-3.5 shrink-0 text-destructive" />
                      )}
                      <span className="font-mono">{result.name}</span>
                      <span
                        className={
                          result.ok
                            ? 'text-muted-foreground'
                            : 'min-w-0 break-all text-destructive'
                        }
                      >
                        {result.ok
                          ? t('applications.import.resultSaved')
                          : result.error}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </>
        )}
      </div>

      <DialogFooter className="flex-row items-center justify-between border-t px-6 py-4">
        <Button
          variant="outline"
          onClick={() => onOpenChange(false)}
          disabled={importing}
        >
          {t('common.actions.cancel')}
        </Button>
        <div className="flex gap-2">
          {results.length > 0 ? (
            <Button onClick={() => onOpenChange(false)}>
              {t('applications.import.done')}
            </Button>
          ) : phase === 'select' ? (
            <Button
              disabled={!space || !canNext}
              onClick={() => setPhase('target')}
            >
              {t('applications.wizard.next')}
            </Button>
          ) : (
            <>
              <Button
                variant="outline"
                disabled={importing}
                onClick={() => setPhase('select')}
              >
                {t('applications.wizard.previous')}
              </Button>
              <Button disabled={!canImport} onClick={() => void runImport()}>
                {importing
                  ? t('applications.import.importing')
                  : t('applications.import.importSaveOnly')}
              </Button>
            </>
          )}
        </div>
      </DialogFooter>
    </DialogContent>
  )
}
