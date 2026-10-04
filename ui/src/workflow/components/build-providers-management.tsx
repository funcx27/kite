// Managed build providers: k8s build providers created from kite's managed
// clusters in the settings UI and injected into the buildfleet runtime without
// a config change or restart. The default provider is marked on its name
// (click to switch); the create/edit dialog also carries the "set as default"
// switch, which upserts/deletes the global build-settings row on save.
import '@/workflow/i18n'

import { useMemo, useState } from 'react'
import { IconPencil, IconPlus, IconTrash } from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { useCluster } from '@/hooks/use-cluster'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
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
import { Separator } from '@/components/ui/separator'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { DeleteConfirmationDialog } from '@/components/delete-confirmation-dialog'

import {
  useBuildProviders,
  useBuildSettings,
  useCreateBuildProviderMutation,
  useDeleteBuildProviderMutation,
  useDeleteBuildSettingMutation,
  useUpdateBuildProviderMutation,
  useUpsertBuildSettingMutation,
  WorkflowApiError,
} from '../api'
import type {
  BuildProvider,
  BuildProviderCache,
  UpdateBuildProviderRequest,
} from '../types'

const dns1123LabelPattern = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

const validDNS1123Label = (value: string) =>
  value.length <= 63 && dns1123LabelPattern.test(value)

const giQuantityPattern = /^(\d+(?:\.\d+)?)Gi$/

// memoryGiOf renders a stored memory quantity ("16Gi") as a Gi number for the
// numeric input; non-Gi quantities (config-side values) have no managed form.
const memoryGiOf = (quantity: string) => {
  const match = giQuantityPattern.exec(quantity)
  return match ? match[1] : ''
}

// Dialog form state; numeric fields stay strings until validation.
interface ProviderForm {
  name: string
  cluster: string
  namespace: string
  // Fleet capacity caps (cpu cores / memory Gi).
  cpu: string
  memory: string
  // Runtime profile name; always one of the API's runtimes ('' never occurs —
  // the default profile is its own entry).
  runtime: string
  // Per-instance build pod shape (cpu cores / memory Gi).
  podCpu: string
  podMemoryGi: string
  cacheEnabled: boolean
  cacheImage: string
  cacheStorageClass: string
  cacheSize: string
  cacheGcSchedule: string
  // Buildkitd readiness wait in seconds; '' = module default.
  readyTimeoutSeconds: string
  setDefault: boolean
}

const emptyForm = (): ProviderForm => ({
  name: '',
  cluster: '',
  namespace: '',
  cpu: '',
  memory: '',
  runtime: 'default',
  podCpu: '2',
  podMemoryGi: '4',
  cacheEnabled: false,
  cacheImage: '',
  cacheStorageClass: '',
  cacheSize: '20',
  cacheGcSchedule: '0 3 * * *',
  readyTimeoutSeconds: '',
  setDefault: false,
})

const formOf = (provider: BuildProvider, isDefault: boolean): ProviderForm => ({
  ...emptyForm(),
  name: provider.name,
  cluster: provider.cluster,
  namespace: provider.namespace,
  cpu: provider.cpu ? String(provider.cpu) : '',
  memory: memoryGiOf(provider.memory),
  runtime: provider.runtime || 'default',
  podCpu: provider.podCpu ? String(provider.podCpu) : '',
  podMemoryGi: provider.podMemoryGi ? String(provider.podMemoryGi) : '',
  cacheEnabled: provider.cache?.enabled ?? false,
  cacheImage: provider.cache?.image ?? '',
  cacheStorageClass: provider.cache?.storageClass ?? '',
  cacheSize: provider.cache ? memoryGiOf(provider.cache.size) : '20',
  cacheGcSchedule: provider.cache?.gcSchedule ?? '0 3 * * *',
  readyTimeoutSeconds: provider.readyTimeoutSeconds
    ? String(provider.readyTimeoutSeconds)
    : '',
  setDefault: isDefault,
})

// Optional positive number field: '' means "leave at the built-in default".
const positiveOrUndefined = (raw: string) => {
  const trimmed = raw.trim()
  if (trimmed === '') return { value: undefined, error: false }
  const n = Number(trimmed)
  return { value: n, error: !Number.isFinite(n) || n <= 0 }
}

export function BuildProvidersManagement() {
  const { t } = useTranslation()

  const { data, isLoading, error } = useBuildProviders()
  const providers = useMemo(() => data?.providers ?? [], [data])
  const configProviders = useMemo(() => data?.configProviders ?? [], [data])
  const runtimes = useMemo(() => data?.runtimes ?? [], [data])
  const { data: settingsData } = useBuildSettings()
  const settings = useMemo(() => settingsData?.settings ?? [], [settingsData])
  const defaultProvider =
    settings.find((s) => s.namespace === '')?.provider ?? ''
  const createMutation = useCreateBuildProviderMutation()
  const updateMutation = useUpdateBuildProviderMutation()
  const deleteMutation = useDeleteBuildProviderMutation()
  const upsertSettingMutation = useUpsertBuildSettingMutation()
  const deleteSettingMutation = useDeleteBuildSettingMutation()

  const { clusters } = useCluster()
  const healthyClusters = useMemo(
    () => clusters.filter((candidate) => !candidate.error),
    [clusters]
  )

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingName, setEditingName] = useState<string | null>(null)
  const [form, setForm] = useState<ProviderForm>(emptyForm)
  const [deleteTarget, setDeleteTarget] = useState<BuildProvider | null>(null)

  // Referenced-by hint only: deletion is never blocked, the run-time error
  // semantics of a dangling provider reference are already defined.
  const referencedCount = useMemo(
    () =>
      deleteTarget
        ? settings.filter(
            (s) => s.namespace !== '' && s.provider === deleteTarget.name
          ).length
        : 0,
    [settings, deleteTarget]
  )

  const notConfigured =
    error instanceof WorkflowApiError && error.status === 404

  const openCreate = () => {
    setForm(emptyForm())
    setEditingName(null)
    setDialogOpen(true)
  }

  const openEdit = (provider: BuildProvider) => {
    setForm(formOf(provider, provider.name === defaultProvider))
    setEditingName(provider.name)
    setDialogOpen(true)
  }

  // validate turns the form into the API request shared by create and update,
  // or null after toasting the first problem.
  const validate = (f: ProviderForm): UpdateBuildProviderRequest | null => {
    const cluster = f.cluster
    const namespace = f.namespace.trim()
    if (cluster === '') {
      toast.error(t('workflow.buildProviders.clusterRequired'))
      return null
    }
    if (!validDNS1123Label(namespace)) {
      toast.error(t('workflow.buildProviders.namespaceInvalid'))
      return null
    }
    const cpu = positiveOrUndefined(f.cpu)
    if (cpu.error) {
      toast.error(t('workflow.buildProviders.cpuInvalid'))
      return null
    }
    const memoryGi = positiveOrUndefined(f.memory)
    if (memoryGi.error) {
      toast.error(t('workflow.buildProviders.memoryInvalid'))
      return null
    }
    const podCpu = positiveOrUndefined(f.podCpu)
    if (podCpu.error) {
      toast.error(t('workflow.buildProviders.podCpuInvalid'))
      return null
    }
    const podMemoryGi = positiveOrUndefined(f.podMemoryGi)
    if (podMemoryGi.error) {
      toast.error(t('workflow.buildProviders.podMemoryGiInvalid'))
      return null
    }
    const ready = f.readyTimeoutSeconds.trim()
    const readyTimeoutSeconds = ready === '' ? undefined : Number(ready)
    if (
      readyTimeoutSeconds !== undefined &&
      (!Number.isInteger(readyTimeoutSeconds) || readyTimeoutSeconds < 0)
    ) {
      toast.error(t('workflow.buildProviders.readyTimeoutInvalid'))
      return null
    }
    let cache: BuildProviderCache | null = null
    if (f.cacheEnabled) {
      const sizeGi = positiveOrUndefined(f.cacheSize)
      if (
        f.cacheImage.trim() === '' ||
        sizeGi.error ||
        sizeGi.value === undefined
      ) {
        toast.error(t('workflow.buildProviders.cacheRequired'))
        return null
      }
      if (f.cacheGcSchedule.trim() === '') {
        toast.error(t('workflow.buildProviders.cacheRequired'))
        return null
      }
      cache = {
        enabled: true,
        image: f.cacheImage.trim(),
        storageClass: f.cacheStorageClass.trim(),
        size: `${sizeGi.value}Gi`,
        gcSchedule: f.cacheGcSchedule.trim(),
      }
    }
    return {
      cluster,
      namespace,
      cpu: cpu.value,
      memory: memoryGi.value === undefined ? undefined : `${memoryGi.value}Gi`,
      runtime: f.runtime,
      readyTimeoutSeconds,
      podCpu: podCpu.value,
      podMemoryGi: podMemoryGi.value,
      cache,
    }
  }

  const submit = () => {
    const req = validate(form)
    if (!req) return
    const name = editingName ?? form.name.trim()
    if (!editingName && !validDNS1123Label(name)) {
      toast.error(t('workflow.buildProviders.nameInvalid'))
      return
    }
    // Provider row first, then the default switch diff — the global row may
    // only reference the provider once it exists.
    const applyDefaultDiff = async () => {
      const wasDefault = defaultProvider === name
      if (form.setDefault && !wasDefault) {
        await upsertSettingMutation.mutateAsync({
          namespace: '',
          provider: name,
        })
      } else if (!form.setDefault && wasDefault) {
        await deleteSettingMutation.mutateAsync('')
      }
    }
    const onError = (err: Error) => toast.error(err.message)
    const finish = async () => {
      try {
        await applyDefaultDiff()
        toast.success(
          editingName
            ? t('workflow.buildProviders.edited')
            : t('workflow.buildProviders.created')
        )
        setDialogOpen(false)
      } catch (err) {
        toast.error(err instanceof Error ? err.message : String(err))
      }
    }
    if (editingName) {
      updateMutation.mutate(
        { name: editingName, req },
        { onSuccess: finish, onError }
      )
      return
    }
    createMutation.mutate({ name, ...req }, { onSuccess: finish, onError })
  }

  const toggleDefault = (name: string) => {
    const onError = (err: Error) => toast.error(err.message)
    if (name === defaultProvider) {
      deleteSettingMutation.mutate('', {
        onSuccess: () =>
          toast.success(t('workflow.buildProviders.defaultUnset')),
        onError,
      })
      return
    }
    upsertSettingMutation.mutate(
      { namespace: '', provider: name },
      {
        onSuccess: () => toast.success(t('workflow.buildProviders.defaultSet')),
        onError,
      }
    )
  }

  const pending =
    createMutation.isPending ||
    updateMutation.isPending ||
    deleteMutation.isPending ||
    upsertSettingMutation.isPending ||
    deleteSettingMutation.isPending

  const providerNameCell = (name: string) => {
    const isDefault = name === defaultProvider
    return (
      <button
        type="button"
        disabled={pending}
        onClick={() => toggleDefault(name)}
        title={
          isDefault
            ? t('workflow.buildProviders.unsetDefaultTitle')
            : t('workflow.buildProviders.setDefaultTitle')
        }
        className={`inline-flex items-center gap-1.5 rounded font-mono text-sm ${
          isDefault
            ? 'font-semibold text-primary'
            : 'text-foreground/80 hover:text-primary'
        }`}
      >
        {name}
        {isDefault && (
          <Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
            {t('workflow.buildProviders.defaultBadge')}
          </Badge>
        )}
      </button>
    )
  }

  // instanceSpecCell renders the per-instance build pod shape, "2 核 · 4Gi";
  // zero/empty on both axes shows an em dash.
  const instanceSpecCell = (provider: BuildProvider) => {
    if (!provider.podCpu && !provider.podMemoryGi) {
      return <span className="text-muted-foreground">—</span>
    }
    return (
      <span className="font-mono text-xs">
        {provider.podCpu
          ? `${provider.podCpu} ${t('workflow.buildProviders.cpuUnit')}`
          : ''}
        {provider.podCpu && provider.podMemoryGi ? ' · ' : ''}
        {provider.podMemoryGi ? `${provider.podMemoryGi}Gi` : ''}
      </span>
    )
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between space-y-0">
        <div className="space-y-1">
          <CardTitle className="flex items-center gap-2">
            {t('workflow.buildProviders.title')}
            <Badge variant="secondary">{t('workflow.globalBadge')}</Badge>
          </CardTitle>
          <CardDescription>
            {t('workflow.buildProviders.description')}
          </CardDescription>
        </div>
        <Button onClick={openCreate}>
          <IconPlus className="mr-1.5 h-4 w-4" />
          {t('workflow.buildProviders.add')}
        </Button>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('workflow.buildProviders.name')}</TableHead>
              <TableHead>{t('workflow.buildProviders.cluster')}</TableHead>
              <TableHead>{t('workflow.buildProviders.namespace')}</TableHead>
              <TableHead>{t('workflow.buildProviders.instanceSpec')}</TableHead>
              <TableHead className="text-right">
                {t('common.fields.actions')}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {providers.map((provider) => (
              <TableRow key={provider.name}>
                <TableCell>{providerNameCell(provider.name)}</TableCell>
                <TableCell>
                  <span className="font-mono">{provider.cluster}</span>
                </TableCell>
                <TableCell>
                  <span className="font-mono">{provider.namespace}</span>
                </TableCell>
                <TableCell>{instanceSpecCell(provider)}</TableCell>
                <TableCell className="text-right">
                  <div className="flex justify-end gap-1.5">
                    <Button
                      variant="ghost"
                      size="icon"
                      className="text-muted-foreground hover:text-primary"
                      disabled={pending}
                      onClick={() => openEdit(provider)}
                      title={t('common.actions.edit')}
                    >
                      <IconPencil className="h-4 w-4" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="text-muted-foreground hover:text-destructive"
                      onClick={() => setDeleteTarget(provider)}
                    >
                      <IconTrash className="h-4 w-4" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
            {configProviders.map((name) => (
              <TableRow key={name}>
                <TableCell>{providerNameCell(name)}</TableCell>
                <TableCell colSpan={2}>
                  <Badge variant="outline">
                    {t('workflow.buildProviders.configProviderTag')}
                  </Badge>
                </TableCell>
                <TableCell>
                  <span className="text-muted-foreground">—</span>
                </TableCell>
                <TableCell className="text-right">
                  <span className="text-muted-foreground">—</span>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {isLoading && (
          <p className="py-4 text-center text-sm text-muted-foreground">
            {t('common.messages.loading')}
          </p>
        )}
        {!isLoading &&
          providers.length === 0 &&
          configProviders.length === 0 && (
            <p className="py-4 text-center text-sm text-muted-foreground">
              {notConfigured
                ? t('workflow.buildProviders.notConfigured')
                : t('workflow.buildProviders.empty')}
            </p>
          )}
      </CardContent>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>
              {editingName
                ? t('workflow.buildProviders.editTitle', { name: editingName })
                : t('workflow.buildProviders.createTitle')}
            </DialogTitle>
            {editingName && (
              <DialogDescription>
                {t('workflow.buildProviders.nameImmutableHint')}
              </DialogDescription>
            )}
          </DialogHeader>
          <div className="max-h-[60vh] space-y-4 overflow-y-auto pr-1">
            <SectionLabel label={t('workflow.buildProviders.basicSection')} />
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label className="text-xs">
                  {t('workflow.buildProviders.name')}
                </Label>
                <Input
                  value={form.name}
                  disabled={editingName !== null}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder={t('workflow.buildProviders.namePlaceholder')}
                  className="font-mono"
                />
                {editingName === null && (
                  <p className="text-[11px] text-muted-foreground">
                    {t('workflow.buildProviders.nameHint')}
                  </p>
                )}
              </div>
              <div className="space-y-1.5">
                <Label className="text-xs">
                  {t('workflow.buildProviders.namespace')}
                </Label>
                <Input
                  value={form.namespace}
                  onChange={(e) =>
                    setForm({ ...form, namespace: e.target.value })
                  }
                  placeholder={t(
                    'workflow.buildProviders.namespacePlaceholder'
                  )}
                  className="font-mono"
                />
                <p className="text-[11px] text-muted-foreground">
                  {t('workflow.buildProviders.namespaceHint')}
                </p>
              </div>
            </div>
            <div className="space-y-1.5">
              <Label className="text-xs">
                {t('workflow.buildProviders.cluster')}
              </Label>
              <Select
                value={form.cluster}
                onValueChange={(cluster) => setForm({ ...form, cluster })}
              >
                <SelectTrigger className="w-full">
                  <SelectValue
                    placeholder={t(
                      'workflow.buildProviders.clusterPlaceholder'
                    )}
                  />
                </SelectTrigger>
                <SelectContent>
                  {healthyClusters.map((cluster) => (
                    <SelectItem key={cluster.name} value={cluster.name}>
                      {cluster.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <Separator />
            <SectionLabel label={t('workflow.buildProviders.runtimeSection')} />
            <div className="space-y-1.5">
              <Select
                value={form.runtime}
                onValueChange={(runtime) => setForm({ ...form, runtime })}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {runtimes.map((runtime) => (
                    <SelectItem key={runtime} value={runtime}>
                      {runtime}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-[11px] text-muted-foreground">
                {t('workflow.buildProviders.runtimeHint')}
              </p>
            </div>

            <Separator />
            <SectionLabel
              label={t('workflow.buildProviders.capacitySection')}
            />
            <div className="grid grid-cols-2 gap-3">
              <NumberField
                label={t('workflow.buildProviders.cpuLimit')}
                unit={t('workflow.buildProviders.cpuUnit')}
                value={form.cpu}
                onChange={(cpu) => setForm({ ...form, cpu })}
                placeholder={t('workflow.buildProviders.cpuPlaceholder')}
              />
              <NumberField
                label={t('workflow.buildProviders.memoryLimit')}
                unit="Gi"
                value={form.memory}
                onChange={(memory) => setForm({ ...form, memory })}
                placeholder={t('workflow.buildProviders.memoryPlaceholder')}
              />
            </div>

            <Separator />
            <SectionLabel label={t('workflow.buildProviders.podSpecSection')} />
            <div className="grid grid-cols-2 gap-3">
              <NumberField
                label={t('workflow.buildProviders.podCpu')}
                unit={t('workflow.buildProviders.cpuUnit')}
                value={form.podCpu}
                onChange={(podCpu) => setForm({ ...form, podCpu })}
              />
              <NumberField
                label={t('workflow.buildProviders.podMemory')}
                unit="Gi"
                value={form.podMemoryGi}
                onChange={(podMemoryGi) => setForm({ ...form, podMemoryGi })}
              />
            </div>
            <p className="text-[11px] text-muted-foreground">
              {t('workflow.buildProviders.podSpecHint')}
            </p>

            <Separator />
            <div className="flex items-center gap-2">
              <Checkbox
                id="provider-cache-enabled"
                checked={form.cacheEnabled}
                onCheckedChange={(checked) =>
                  setForm({ ...form, cacheEnabled: checked === true })
                }
              />
              <Label
                htmlFor="provider-cache-enabled"
                className="text-xs font-medium"
              >
                {t('workflow.buildProviders.cacheEnable')}
              </Label>
            </div>
            {form.cacheEnabled && (
              <div className="space-y-3">
                <div className="grid grid-cols-2 gap-3">
                  <div className="space-y-1.5">
                    <Label className="text-xs">
                      {t('workflow.buildProviders.cacheImage')}
                    </Label>
                    <Input
                      value={form.cacheImage}
                      onChange={(e) =>
                        setForm({ ...form, cacheImage: e.target.value })
                      }
                      placeholder="registry.local:5000/buildkit-cache"
                      className="font-mono text-xs"
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label className="text-xs">
                      {t('workflow.buildProviders.cacheStorageClass')}
                    </Label>
                    <Input
                      value={form.cacheStorageClass}
                      onChange={(e) =>
                        setForm({ ...form, cacheStorageClass: e.target.value })
                      }
                      className="font-mono text-xs"
                    />
                  </div>
                  <NumberField
                    label={t('workflow.buildProviders.cacheSize')}
                    unit="Gi"
                    value={form.cacheSize}
                    onChange={(cacheSize) => setForm({ ...form, cacheSize })}
                  />
                  <div className="space-y-1.5">
                    <Label className="text-xs">
                      {t('workflow.buildProviders.cacheGcSchedule')}
                    </Label>
                    <Input
                      value={form.cacheGcSchedule}
                      onChange={(e) =>
                        setForm({ ...form, cacheGcSchedule: e.target.value })
                      }
                      className="font-mono text-xs"
                    />
                  </div>
                </div>
                <p className="text-[11px] text-muted-foreground">
                  {t('workflow.buildProviders.cacheHint')}
                </p>
              </div>
            )}

            <Separator />
            <SectionLabel
              label={t('workflow.buildProviders.advancedSection')}
            />
            <div className="space-y-1.5">
              <NumberField
                label={t('workflow.buildProviders.readyTimeout')}
                unit="s"
                value={form.readyTimeoutSeconds}
                onChange={(readyTimeoutSeconds) =>
                  setForm({ ...form, readyTimeoutSeconds })
                }
              />
              <p className="text-[11px] text-muted-foreground">
                {t('workflow.buildProviders.readyTimeoutHint')}
              </p>
            </div>

            <Separator />
            <div className="flex items-center gap-2">
              <Checkbox
                id="provider-set-default"
                checked={form.setDefault}
                onCheckedChange={(checked) =>
                  setForm({ ...form, setDefault: checked === true })
                }
              />
              <Label
                htmlFor="provider-set-default"
                className="text-xs font-medium"
              >
                {t('workflow.buildProviders.setDefaultLabel')}
              </Label>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              {t('common.actions.cancel')}
            </Button>
            <Button
              disabled={
                pending ||
                form.cluster === '' ||
                (!editingName && form.name.trim() === '')
              }
              onClick={submit}
            >
              {editingName
                ? t('common.actions.confirm')
                : t('common.actions.create')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <DeleteConfirmationDialog
        open={deleteTarget !== null}
        onOpenChange={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (!deleteTarget) return
          deleteMutation.mutate(deleteTarget.name, {
            onSuccess: () => {
              toast.success(t('workflow.buildProviders.deleted'))
              setDeleteTarget(null)
            },
            onError: (err) => toast.error(err.message),
          })
        }}
        resourceName={deleteTarget?.name ?? ''}
        resourceType={t('workflow.buildProviders.deleteType')}
        additionalNote={
          referencedCount > 0
            ? t('workflow.buildProviders.deleteReferenced', {
                count: referencedCount,
              })
            : t('workflow.buildProviders.deleteDescription')
        }
      />
    </Card>
  )
}

function SectionLabel({ label }: { label: string }) {
  return (
    <p className="-mb-2 text-xs font-semibold text-muted-foreground">{label}</p>
  )
}

// NumberField is a labeled numeric input with a trailing unit.
function NumberField({
  label,
  unit,
  value,
  onChange,
  placeholder,
}: {
  label: string
  unit: string
  value: string
  onChange: (value: string) => void
  placeholder?: string
}) {
  return (
    <div className="space-y-1.5">
      <Label className="text-xs">{label}</Label>
      <div className="flex items-center gap-1.5">
        <Input
          type="number"
          step="any"
          min="0"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder}
        />
        <span className="shrink-0 text-xs text-muted-foreground">{unit}</span>
      </div>
    </div>
  )
}
