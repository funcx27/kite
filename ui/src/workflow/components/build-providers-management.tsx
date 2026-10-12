// Managed build providers: k8s (managed-cluster) and docker (the kite host's
// daemon) build providers created in the settings UI and injected into the
// buildfleet runtime without a config change or restart. The default provider
// is marked with a badge on its name; the create/edit dialog also carries the
// "set as default" switch, which upserts/deletes the global build-settings
// row on save.
import '@/workflow/i18n'

import { useMemo, useState } from 'react'
import {
  DockerIcon,
  KubernetesIcon,
} from '@/application/components/brand-icons'
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
import { Switch } from '@/components/ui/switch'
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
  useSetBuildProviderEnabledMutation,
  useUpdateBuildProviderMutation,
  useUpsertBuildSettingMutation,
  WorkflowApiError,
} from '../api'
import type {
  BuildProvider,
  BuildProviderCache,
  BuildProviderDefaults,
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
  type: 'k8s' | 'docker'
  name: string
  cluster: string
  namespace: string
  // Docker daemon endpoint + optional mutual-TLS material (host file paths).
  address: string
  tlsCa: string
  tlsCert: string
  tlsKey: string
  tlsInsecure: boolean
  // Fleet capacity caps (cpu cores / memory Gi).
  cpu: string
  memory: string
  // Build mode sent as the provider runtime: 'default' (rootful) or
  // 'rootless'.
  runtime: string
  // Per-instance build pod shape (cpu cores / memory Gi). k8s only.
  podCpu: string
  podMemoryGi: string
  cacheEnabled: boolean
  cacheImage: string
  cacheStorageClass: string
  cacheSize: string
  cacheGcSchedule: string
  // Buildkitd readiness wait in seconds; k8s only.
  readyTimeoutSeconds: string
  setDefault: boolean
}

// Mirrors the backend's built-in defaults (buildfleet providerDefaults and
// the module defaults) for the case the API is unreachable.
const DEFAULTS_FALLBACK: BuildProviderDefaults = {
  readyTimeoutSeconds: 60,
  podCpu: 2,
  podMemoryGi: 4,
  cacheImage: 'registry:3',
  cacheSize: '20Gi',
  cacheGcSchedule: '0 3 * * *',
  dockerCpu: 8,
  dockerMemory: '16Gi',
}

// emptyForm prefills every default-carrying field from the API's defaults so
// the dialog shows the effective values instead of empty boxes.
const emptyForm = (defaults: BuildProviderDefaults): ProviderForm => ({
  type: 'k8s',
  name: '',
  cluster: '',
  namespace: '',
  address: 'unix:///var/run/docker.sock',
  tlsCa: '',
  tlsCert: '',
  tlsKey: '',
  tlsInsecure: false,
  cpu: '',
  memory: '',
  runtime: 'default',
  podCpu: defaults.podCpu ? String(defaults.podCpu) : '',
  podMemoryGi: defaults.podMemoryGi ? String(defaults.podMemoryGi) : '',
  cacheEnabled: false,
  cacheImage: defaults.cacheImage,
  cacheStorageClass: '',
  cacheSize: memoryGiOf(defaults.cacheSize),
  cacheGcSchedule: defaults.cacheGcSchedule,
  readyTimeoutSeconds: defaults.readyTimeoutSeconds
    ? String(defaults.readyTimeoutSeconds)
    : '',
  setDefault: false,
})

const formOf = (
  provider: BuildProvider,
  isDefault: boolean,
  defaults: BuildProviderDefaults
): ProviderForm => ({
  ...emptyForm(defaults),
  type: provider.type,
  name: provider.name,
  cluster: provider.cluster,
  namespace: provider.namespace,
  address: provider.address,
  tlsCa: provider.tls?.ca ?? '',
  tlsCert: provider.tls?.cert ?? '',
  tlsKey: provider.tls?.key ?? '',
  tlsInsecure: provider.tls?.insecureSkipVerify ?? false,
  cpu: provider.cpu ? String(provider.cpu) : '',
  memory: memoryGiOf(provider.memory),
  runtime: provider.runtime === 'rootless' ? 'rootless' : 'default',
  podCpu: provider.podCpu ? String(provider.podCpu) : '',
  podMemoryGi: provider.podMemoryGi ? String(provider.podMemoryGi) : '',
  cacheEnabled: provider.cache?.enabled ?? false,
  cacheImage: provider.cache?.image ?? '',
  cacheStorageClass: provider.cache?.storageClass ?? '',
  cacheSize: provider.cache
    ? memoryGiOf(provider.cache.size)
    : memoryGiOf(defaults.cacheSize),
  cacheGcSchedule: provider.cache?.gcSchedule ?? defaults.cacheGcSchedule,
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
  const defaults = useMemo(() => data?.defaults ?? DEFAULTS_FALLBACK, [data])
  const { data: settingsData } = useBuildSettings()
  const settings = useMemo(() => settingsData?.settings ?? [], [settingsData])
  const defaultProvider =
    settings.find((s) => s.namespace === '')?.provider ?? ''
  const createMutation = useCreateBuildProviderMutation()
  const updateMutation = useUpdateBuildProviderMutation()
  const deleteMutation = useDeleteBuildProviderMutation()
  const enabledMutation = useSetBuildProviderEnabledMutation()
  const upsertSettingMutation = useUpsertBuildSettingMutation()
  const deleteSettingMutation = useDeleteBuildSettingMutation()

  const { clusters } = useCluster()
  const healthyClusters = useMemo(
    () => clusters.filter((candidate) => !candidate.error),
    [clusters]
  )

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingName, setEditingName] = useState<string | null>(null)
  const [form, setForm] = useState<ProviderForm>(() =>
    emptyForm(DEFAULTS_FALLBACK)
  )
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
    setForm(emptyForm(defaults))
    setEditingName(null)
    setDialogOpen(true)
  }

  const openEdit = (provider: BuildProvider) => {
    setForm(formOf(provider, provider.name === defaultProvider, defaults))
    setEditingName(provider.name)
    setDialogOpen(true)
  }

  // switchType seeds the docker capacity caps from the API defaults the first
  // time the user flips to docker on an untouched capacity pair.
  const switchType = (type: 'k8s' | 'docker') => {
    setForm((prev) => {
      if (
        type === 'docker' &&
        prev.type === 'k8s' &&
        prev.cpu === '' &&
        prev.memory === ''
      ) {
        return {
          ...prev,
          type,
          cpu: defaults.dockerCpu ? String(defaults.dockerCpu) : '',
          memory: memoryGiOf(defaults.dockerMemory),
        }
      }
      return { ...prev, type }
    })
  }

  // validate turns the form into the API request shared by create and update,
  // or null after toasting the first problem.
  const validate = (f: ProviderForm): UpdateBuildProviderRequest | null => {
    const isDocker = f.type === 'docker'
    const cluster = f.cluster
    const namespace = f.namespace.trim()
    if (!isDocker) {
      if (cluster === '') {
        toast.error(t('workflow.buildProviders.clusterRequired'))
        return null
      }
      if (!validDNS1123Label(namespace)) {
        toast.error(t('workflow.buildProviders.namespaceInvalid'))
        return null
      }
    }
    const address = f.address.trim()
    if (
      isDocker &&
      address !== '' &&
      !address.startsWith('unix://') &&
      !address.startsWith('tcp://')
    ) {
      toast.error(t('workflow.buildProviders.addressInvalid'))
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
      !isDocker &&
      readyTimeoutSeconds !== undefined &&
      (!Number.isInteger(readyTimeoutSeconds) || readyTimeoutSeconds < 0)
    ) {
      toast.error(t('workflow.buildProviders.readyTimeoutInvalid'))
      return null
    }
    let cache: BuildProviderCache | null = null
    if (!isDocker && f.cacheEnabled) {
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
    const tls =
      isDocker &&
      (f.tlsCa !== '' || f.tlsCert !== '' || f.tlsKey !== '' || f.tlsInsecure)
        ? {
            ca: f.tlsCa.trim(),
            cert: f.tlsCert.trim(),
            key: f.tlsKey.trim(),
            insecureSkipVerify: f.tlsInsecure,
          }
        : null
    return {
      type: f.type,
      cluster: isDocker ? undefined : cluster,
      namespace: isDocker ? undefined : namespace,
      address: isDocker ? address : undefined,
      tls,
      cpu: cpu.value,
      memory: memoryGi.value === undefined ? undefined : `${memoryGi.value}Gi`,
      runtime: f.runtime,
      readyTimeoutSeconds: isDocker ? undefined : readyTimeoutSeconds,
      podCpu: isDocker ? undefined : podCpu.value,
      podMemoryGi: isDocker ? undefined : podMemoryGi.value,
      cache: isDocker ? null : cache,
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

  const pending =
    createMutation.isPending ||
    updateMutation.isPending ||
    deleteMutation.isPending ||
    enabledMutation.isPending ||
    upsertSettingMutation.isPending ||
    deleteSettingMutation.isPending

  // capacityCell renders the provider's fleet capacity caps, "4 核 · 8Gi";
  // zero/empty on both axes shows an em dash.
  const capacityCell = (provider: BuildProvider) => {
    const memoryGi = memoryGiOf(provider.memory)
    if (!provider.cpu && !memoryGi) {
      return <span className="text-muted-foreground">—</span>
    }
    return (
      <span className="font-mono text-xs">
        {provider.cpu
          ? `${provider.cpu} ${t('workflow.buildProviders.cpuUnit')}`
          : ''}
        {provider.cpu && memoryGi ? ' · ' : ''}
        {memoryGi ? `${memoryGi}Gi` : ''}
      </span>
    )
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between space-y-0">
        <div className="space-y-1">
          <CardTitle className="flex items-center gap-2">
            {t('workflow.buildProviders.title')}
          </CardTitle>
          <CardDescription>
            {t('workflow.buildProviders.description')}
          </CardDescription>
        </div>
        <Button
          size="icon"
          onClick={openCreate}
          title={t('workflow.buildProviders.add')}
        >
          <IconPlus className="h-4 w-4" />
        </Button>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('workflow.buildProviders.name')}</TableHead>
              <TableHead>{t('workflow.buildProviders.cluster')}</TableHead>
              <TableHead>{t('workflow.buildProviders.namespace')}</TableHead>
              <TableHead>
                {t('workflow.buildProviders.capacitySection')}
              </TableHead>
              <TableHead className="text-right">
                {t('common.fields.actions')}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {providers.map((provider) => (
              <TableRow key={provider.name}>
                <TableCell>
                  <span
                    className="mr-1.5 inline-flex align-middle"
                    title={
                      provider.type === 'docker'
                        ? t('workflow.buildProviders.typeDocker')
                        : t('workflow.buildProviders.typeK8s')
                    }
                  >
                    {provider.type === 'docker' ? (
                      <DockerIcon className="h-4 w-4" />
                    ) : (
                      <KubernetesIcon className="h-4 w-4" />
                    )}
                  </span>
                  <span
                    className={
                      provider.enabled === false
                        ? 'font-mono text-sm text-muted-foreground'
                        : 'font-mono text-sm'
                    }
                  >
                    {provider.name}
                  </span>
                  {provider.name === defaultProvider && (
                    <Badge variant="secondary" className="ml-2">
                      {t('workflow.buildProviders.defaultTag')}
                    </Badge>
                  )}
                  {provider.enabled === false && (
                    <Badge variant="outline" className="ml-2">
                      {t('workflow.buildProviders.disabledTag')}
                    </Badge>
                  )}
                </TableCell>
                <TableCell>
                  <span className="font-mono">{provider.cluster || '—'}</span>
                </TableCell>
                <TableCell>
                  <span className="font-mono">{provider.namespace || '—'}</span>
                </TableCell>
                <TableCell>{capacityCell(provider)}</TableCell>
                <TableCell className="text-right">
                  <div className="flex items-center justify-end gap-1.5">
                    <Switch
                      checked={provider.enabled !== false}
                      disabled={pending}
                      onCheckedChange={(enabled) =>
                        enabledMutation.mutate(
                          { name: provider.name, enabled },
                          { onError: (err) => toast.error(err.message) }
                        )
                      }
                      title={t('workflow.buildProviders.toggleEnabled')}
                    />
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
                <TableCell>
                  <span className="font-mono text-sm">{name}</span>
                  {name === defaultProvider && (
                    <Badge variant="secondary" className="ml-2">
                      {t('workflow.buildProviders.defaultTag')}
                    </Badge>
                  )}
                </TableCell>
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
        <DialogContent className="sm:max-w-2xl">
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
                  {t('workflow.buildProviders.type')}
                </Label>
                {editingName === null ? (
                  <Select value={form.type} onValueChange={switchType}>
                    <SelectTrigger className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="k8s">
                        {t('workflow.buildProviders.typeK8s')}
                      </SelectItem>
                      <SelectItem value="docker">
                        {t('workflow.buildProviders.typeDocker')}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                ) : (
                  <Input
                    value={
                      form.type === 'docker'
                        ? t('workflow.buildProviders.typeDocker')
                        : t('workflow.buildProviders.typeK8s')
                    }
                    disabled
                  />
                )}
                {editingName === null && (
                  <p className="text-[11px] text-muted-foreground">
                    {t('workflow.buildProviders.typeHint')}
                  </p>
                )}
              </div>
            </div>
            {form.type === 'docker' ? (
              <>
                <div className="space-y-1.5">
                  <Label className="text-xs">
                    {t('workflow.buildProviders.address')}
                  </Label>
                  <Input
                    value={form.address}
                    onChange={(e) =>
                      setForm({ ...form, address: e.target.value })
                    }
                    placeholder="unix:///var/run/docker.sock"
                    className="font-mono"
                  />
                  <p className="text-[11px] text-muted-foreground">
                    {t('workflow.buildProviders.addressHint')}
                  </p>
                </div>
                <div className="space-y-2">
                  <p className="text-xs font-semibold text-muted-foreground">
                    {t('workflow.buildProviders.tlsSection')}
                  </p>
                  <div className="grid grid-cols-2 gap-3">
                    <div className="space-y-1.5">
                      <Label className="text-xs">
                        {t('workflow.buildProviders.tlsCa')}
                      </Label>
                      <Input
                        value={form.tlsCa}
                        onChange={(e) =>
                          setForm({ ...form, tlsCa: e.target.value })
                        }
                        className="font-mono text-xs"
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label className="text-xs">
                        {t('workflow.buildProviders.tlsCert')}
                      </Label>
                      <Input
                        value={form.tlsCert}
                        onChange={(e) =>
                          setForm({ ...form, tlsCert: e.target.value })
                        }
                        className="font-mono text-xs"
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label className="text-xs">
                        {t('workflow.buildProviders.tlsKey')}
                      </Label>
                      <Input
                        value={form.tlsKey}
                        onChange={(e) =>
                          setForm({ ...form, tlsKey: e.target.value })
                        }
                        className="font-mono text-xs"
                      />
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    <Checkbox
                      id="provider-tls-insecure"
                      checked={form.tlsInsecure}
                      onCheckedChange={(checked) =>
                        setForm({ ...form, tlsInsecure: checked === true })
                      }
                    />
                    <Label
                      htmlFor="provider-tls-insecure"
                      className="text-xs font-medium"
                    >
                      {t('workflow.buildProviders.tlsInsecure')}
                    </Label>
                  </div>
                </div>
              </>
            ) : (
              <>
                <div className="grid grid-cols-2 gap-3">
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
              </>
            )}

            <Separator />
            <SectionLabel label={t('workflow.buildProviders.modeSection')} />
            <div className="space-y-1.5">
              <Select
                value={form.runtime}
                onValueChange={(runtime) => setForm({ ...form, runtime })}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="default">
                    {t('workflow.buildProviders.modeRootful')}
                  </SelectItem>
                  <SelectItem value="rootless">
                    {t('workflow.buildProviders.modeRootless')}
                  </SelectItem>
                </SelectContent>
              </Select>
              <p className="text-[11px] text-muted-foreground">
                {t('workflow.buildProviders.modeHint')}
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

            {form.type === 'k8s' && (
              <>
                <Separator />
                <SectionLabel
                  label={t('workflow.buildProviders.podSpecSection')}
                />
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
                    onChange={(podMemoryGi) =>
                      setForm({ ...form, podMemoryGi })
                    }
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
                            setForm({
                              ...form,
                              cacheStorageClass: e.target.value,
                            })
                          }
                          className="font-mono text-xs"
                        />
                      </div>
                      <NumberField
                        label={t('workflow.buildProviders.cacheSize')}
                        unit="Gi"
                        value={form.cacheSize}
                        onChange={(cacheSize) =>
                          setForm({ ...form, cacheSize })
                        }
                      />
                      <div className="space-y-1.5">
                        <Label className="text-xs">
                          {t('workflow.buildProviders.cacheGcSchedule')}
                        </Label>
                        <Input
                          value={form.cacheGcSchedule}
                          onChange={(e) =>
                            setForm({
                              ...form,
                              cacheGcSchedule: e.target.value,
                            })
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
              </>
            )}

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
                (form.type === 'k8s' && form.cluster === '') ||
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
  return <p className="text-xs font-semibold text-muted-foreground">{label}</p>
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
