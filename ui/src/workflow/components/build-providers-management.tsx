// Managed build providers: k8s build providers created from kite's managed
// clusters in the settings UI and injected into the buildfleet runtime without
// a config change or restart. Provider choices of the build settings surface
// (names + referenced-by hints) come from the same module API family.
import '@/workflow/i18n'

import { useMemo, useState } from 'react'
import { IconPlus, IconTrash } from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { useCluster } from '@/hooks/use-cluster'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
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
  WorkflowApiError,
} from '../api'
import type { BuildProvider } from '../types'

const dns1123LabelPattern = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

const validDNS1123Label = (value: string) =>
  value.length <= 63 && dns1123LabelPattern.test(value)

// One row being added; managed providers carry no edit — recreate instead.
interface DraftRow {
  name: string
  cluster: string
  namespace: string
  cpu: string
  memory: string
}

const emptyDraft: DraftRow = {
  name: '',
  cluster: '',
  namespace: '',
  cpu: '',
  memory: '',
}

export function BuildProvidersManagement() {
  const { t } = useTranslation()

  const { data, isLoading, error } = useBuildProviders()
  const providers = data?.providers ?? []
  const configProviders = data?.configProviders ?? []
  const { data: settingsData } = useBuildSettings()
  const settings = settingsData?.settings ?? []
  const createMutation = useCreateBuildProviderMutation()
  const deleteMutation = useDeleteBuildProviderMutation()

  const { clusters } = useCluster()
  const healthyClusters = useMemo(
    () => clusters.filter((candidate) => !candidate.error),
    [clusters]
  )

  const [draft, setDraft] = useState<DraftRow | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<BuildProvider | null>(null)

  // Referenced-by hint only: deletion is never blocked, the run-time error
  // semantics of a dangling provider reference are already defined.
  const referencedCount = useMemo(
    () =>
      deleteTarget
        ? settings.filter((setting) => setting.provider === deleteTarget.name)
            .length
        : 0,
    [settings, deleteTarget]
  )

  const notConfigured =
    error instanceof WorkflowApiError && error.status === 404

  const save = () => {
    if (!draft) return
    const name = draft.name.trim()
    const namespace = draft.namespace.trim()
    const cpu = draft.cpu.trim() === '' ? undefined : Number(draft.cpu)
    const memory = draft.memory.trim() === '' ? undefined : draft.memory.trim()
    if (!validDNS1123Label(name)) {
      toast.error(t('workflow.buildProviders.nameInvalid'))
      return
    }
    if (draft.cluster === '') {
      toast.error(t('workflow.buildProviders.clusterRequired'))
      return
    }
    if (!validDNS1123Label(namespace)) {
      toast.error(t('workflow.buildProviders.namespaceInvalid'))
      return
    }
    if (cpu !== undefined && (!Number.isFinite(cpu) || cpu <= 0)) {
      toast.error(t('workflow.buildProviders.cpuInvalid'))
      return
    }
    createMutation.mutate(
      { name, cluster: draft.cluster, namespace, cpu, memory },
      {
        onSuccess: () => {
          toast.success(t('workflow.buildProviders.created'))
          setDraft(null)
        },
        onError: (err) => toast.error(err.message),
      }
    )
  }

  const pending = createMutation.isPending || deleteMutation.isPending

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between space-y-0">
        <div className="space-y-1">
          <CardTitle>{t('workflow.buildProviders.title')}</CardTitle>
          <CardDescription>
            {t('workflow.buildProviders.description')}
          </CardDescription>
        </div>
        <Button onClick={() => setDraft(emptyDraft)} disabled={draft !== null}>
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
              <TableHead>{t('workflow.buildProviders.resources')}</TableHead>
              <TableHead className="text-right">
                {t('common.fields.actions')}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {draft && (
              <TableRow>
                <BuildProviderEditCells
                  draft={draft}
                  clusters={healthyClusters.map((c) => c.name)}
                  onChange={setDraft}
                />
                <TableCell className="text-right">
                  <div className="flex justify-end gap-1.5">
                    <Button size="sm" disabled={pending} onClick={save}>
                      {t('common.actions.confirm')}
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setDraft(null)}
                    >
                      {t('common.actions.cancel')}
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            )}
            {providers.map((provider) => (
              <TableRow key={provider.name}>
                <TableCell>
                  <span className="font-mono">{provider.name}</span>
                </TableCell>
                <TableCell>
                  <span className="font-mono">{provider.cluster}</span>
                </TableCell>
                <TableCell>
                  <span className="font-mono">{provider.namespace}</span>
                </TableCell>
                <TableCell>
                  {provider.cpu || provider.memory ? (
                    <span className="font-mono text-xs">
                      {provider.cpu ? `${provider.cpu} CPU` : ''}
                      {provider.cpu && provider.memory ? ' · ' : ''}
                      {provider.memory || ''}
                    </span>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
                <TableCell className="text-right">
                  <div className="flex justify-end gap-1.5">
                    <Button
                      variant="ghost"
                      size="icon"
                      className="text-muted-foreground hover:text-destructive"
                      disabled={draft !== null}
                      onClick={() => setDeleteTarget(provider)}
                    >
                      <IconTrash className="h-4 w-4" />
                    </Button>
                  </div>
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
        {!isLoading && providers.length === 0 && !draft && (
          <p className="py-4 text-center text-sm text-muted-foreground">
            {notConfigured
              ? t('workflow.buildProviders.notConfigured')
              : t('workflow.buildProviders.empty')}
          </p>
        )}
        {configProviders.length > 0 && (
          <p className="mt-2 text-xs text-muted-foreground">
            {t('workflow.buildProviders.configProvidersHint', {
              names: configProviders.join(', '),
            })}
          </p>
        )}
      </CardContent>

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

// BuildProviderEditCells renders the inputs of a draft row: name, target
// cluster (kite clusters only — the kubeconfig is exported from there) and
// the namespace receiving buildkitd pods, plus optional capacity caps.
function BuildProviderEditCells({
  draft,
  clusters,
  onChange,
}: {
  draft: DraftRow
  clusters: string[]
  onChange: (draft: DraftRow) => void
}) {
  const { t } = useTranslation()
  return (
    <>
      <TableCell>
        <div className="space-y-1">
          <Input
            value={draft.name}
            onChange={(e) => onChange({ ...draft, name: e.target.value })}
            placeholder={t('workflow.buildProviders.namePlaceholder')}
            className="w-44 font-mono"
          />
          <p className="text-[11px] text-muted-foreground">
            {t('workflow.buildProviders.nameHint')}
          </p>
        </div>
      </TableCell>
      <TableCell>
        <Select
          value={draft.cluster}
          onValueChange={(cluster) => onChange({ ...draft, cluster })}
        >
          <SelectTrigger className="w-44">
            <SelectValue
              placeholder={t('workflow.buildProviders.clusterPlaceholder')}
            />
          </SelectTrigger>
          <SelectContent>
            {clusters.map((cluster) => (
              <SelectItem key={cluster} value={cluster}>
                {cluster}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </TableCell>
      <TableCell>
        <div className="space-y-1">
          <Input
            value={draft.namespace}
            onChange={(e) => onChange({ ...draft, namespace: e.target.value })}
            placeholder={t('workflow.buildProviders.namespacePlaceholder')}
            className="w-44 font-mono"
          />
          <p className="text-[11px] text-muted-foreground">
            {t('workflow.buildProviders.namespaceHint')}
          </p>
        </div>
      </TableCell>
      <TableCell>
        <div className="flex w-44 flex-col gap-1.5">
          <Input
            type="number"
            step="any"
            min="0"
            value={draft.cpu}
            onChange={(e) => onChange({ ...draft, cpu: e.target.value })}
            placeholder={t('workflow.buildProviders.cpuPlaceholder')}
            className="h-8"
          />
          <Input
            value={draft.memory}
            onChange={(e) => onChange({ ...draft, memory: e.target.value })}
            placeholder={t('workflow.buildProviders.memoryPlaceholder')}
            className="h-8 font-mono"
          />
        </div>
      </TableCell>
    </>
  )
}
