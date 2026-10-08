// Per-space resource quotas: one row per cluster that reports the space
// (namespace exists there), with separate CPU and memory columns showing
// the "used/allocated" ratio. Unconfigured clusters get a "set quota"
// action; editing opens a one-row dialog shaped like the create-space
// dialog. The upsert is idempotent per cluster, so a failed save keeps the
// dialog open with the entered values for retry.
import { useEffect, useState } from 'react'
import { useSpaceQuotas, useUpsertSpaceQuotasMutation } from '@/application/api'
import { quotaFieldValid, type QuotaFieldValue } from '@/application/lib'
import type { SpaceQuotaStatus } from '@/application/types'
import {
  cpuQuantityToCores,
  formatCores,
  formatGiB,
  memoryQuantityToGiB,
  useClusterCapacity,
} from '@/application/use-cluster-capacity'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { translateError } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

// Shared per-cluster quota editor: one compact row of plain-number inputs
// whose unit suffix carries the cluster's allocatable total ("/ 10 core"),
// so the allocation is judged against the capacity right where it is typed;
// the bare unit shows when the nodes cannot be read (permission, offline).
export function ClusterQuotaFields({
  cluster,
  value,
  onChange,
  disabled = false,
}: {
  cluster: string
  value: QuotaFieldValue
  onChange: (next: QuotaFieldValue) => void
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const { data } = useClusterCapacity(cluster)
  const hasCapacity = !!data && data.nodes > 0
  const cpuSuffix = hasCapacity
    ? `/ ${formatCores(data.cpuCores)} core`
    : 'core'
  const memSuffix = hasCapacity ? `/ ${formatGiB(data.memoryGiB)} GiB` : 'GiB'
  const suffixField = (key: 'cpu' | 'memory', suffix: string) => (
    <div className="min-w-0 flex-1">
      <div className="relative">
        <Input
          type="number"
          min={0}
          step="any"
          disabled={disabled}
          className="px-2.5 pr-14"
          value={value[key]}
          onChange={(event) =>
            onChange({ ...value, [key]: event.target.value })
          }
          placeholder="0"
        />
        <span className="pointer-events-none absolute inset-y-0 right-2 flex items-center whitespace-nowrap text-xs text-muted-foreground">
          {suffix}
        </span>
      </div>
    </div>
  )
  return (
    <div
      className="flex flex-1 gap-2"
      title={
        hasCapacity
          ? t('applications.spaceQuota.capacityHint', {
              cpu: formatCores(data.cpuCores),
              memory: formatGiB(data.memoryGiB),
              nodes: data.nodes,
            })
          : undefined
      }
    >
      {suffixField('cpu', cpuSuffix)}
      {suffixField('memory', memSuffix)}
    </div>
  )
}

// CPU and memory cells of the settings table: one "used/allocated" ratio
// per unit ("0/1 core"); a quota overshooting the cluster's allocatable
// total tints amber with a tooltip. Status rows span both columns.
function UsageCells({ quota }: { quota: SpaceQuotaStatus }) {
  const { t } = useTranslation()
  const { data } = useClusterCapacity(quota.cluster)
  if (quota.unauthorized) {
    return (
      <TableCell colSpan={2} className="text-muted-foreground">
        {t('applications.spaceQuota.unauthorized')}
      </TableCell>
    )
  }
  if (quota.error) {
    return (
      <TableCell colSpan={2} className="text-destructive">
        {quota.error}
      </TableCell>
    )
  }
  if (quota.unconfigured) {
    return (
      <TableCell colSpan={2} className="text-muted-foreground">
        {t('applications.spaceQuota.unconfigured')}
      </TableCell>
    )
  }
  const capacity = data && data.nodes > 0 ? data : undefined
  const cell = (
    used: number | undefined,
    allocated: number | undefined,
    unit: string,
    format: (n: number) => string,
    over: boolean
  ) => (
    <TableCell
      className={
        over ? 'font-mono text-amber-600 dark:text-amber-400' : 'font-mono'
      }
      title={over ? t('applications.spaceQuota.overCapacity') : undefined}
    >
      {allocated === undefined
        ? '—'
        : `${format(used ?? 0)}/${format(allocated)} ${unit}`}
    </TableCell>
  )
  const cores = cpuQuantityToCores(quota.cpu)
  const gib = memoryQuantityToGiB(quota.memory)
  return (
    <>
      {cell(
        cpuQuantityToCores(quota.usedCpu),
        cores,
        'core',
        formatCores,
        capacity !== undefined && (cores ?? 0) > capacity.cpuCores
      )}
      {cell(
        memoryQuantityToGiB(quota.usedMemory),
        gib,
        'GiB',
        formatGiB,
        capacity !== undefined && (gib ?? 0) > capacity.memoryGiB
      )}
    </>
  )
}

// Read-only "quota / cluster total" pair for clusters whose space already
// carries a quota (create-space dialog); the two spans sit where the edit
// inputs would, so configured and editable rows align.
export function ConfiguredQuota({ quota }: { quota: SpaceQuotaStatus }) {
  const { data } = useClusterCapacity(quota.cluster)
  const denom = data && data.nodes > 0 ? data : undefined
  const cls = 'flex-1 text-right font-mono text-sm text-muted-foreground'
  const cores = cpuQuantityToCores(quota.cpu)
  const gib = memoryQuantityToGiB(quota.memory)
  return (
    <>
      <span className={cls}>
        {cores === undefined
          ? '—'
          : `${formatCores(cores)}${denom ? `/${formatCores(denom.cpuCores)}` : ''} core`}
      </span>
      <span className={cls}>
        {gib === undefined
          ? '—'
          : `${formatGiB(gib)}${denom ? `/${formatGiB(denom.memoryGiB)}` : ''} GiB`}
      </span>
    </>
  )
}

export function SpaceQuotaSettings({ space }: { space: string }) {
  const { t } = useTranslation()
  const { data, isLoading } = useSpaceQuotas(space)
  const upsertMutation = useUpsertSpaceQuotasMutation()
  const [editing, setEditing] = useState<SpaceQuotaStatus | null>(null)
  const [editValue, setEditValue] = useState<QuotaFieldValue>({
    cpu: '',
    memory: '',
  })

  // Refill the edit dialog from the row each time it opens: existing hard
  // limits prefill as core / GiB numbers, unconfigured clusters start empty.
  useEffect(() => {
    if (!editing) return
    const cores = cpuQuantityToCores(editing.cpu)
    const gib = memoryQuantityToGiB(editing.memory)
    setEditValue({
      cpu: cores === undefined ? '' : formatCores(cores),
      memory: gib === undefined ? '' : formatGiB(gib),
    })
  }, [editing])

  const save = async () => {
    if (!editing || !quotaFieldValid(editValue)) return
    try {
      const result = await upsertMutation.mutateAsync({
        name: space,
        quotas: [
          {
            cluster: editing.cluster,
            cpu: String(Number(editValue.cpu)),
            memory: `${Number(editValue.memory)}Gi`,
          },
        ],
      })
      const failed = result.failed.find(
        (entry) => entry.cluster === editing.cluster
      )
      if (failed) {
        toast.error(
          t('applications.spaceQuota.saveFailed', {
            cluster: editing.cluster,
            error: failed.error,
          })
        )
        return
      }
      toast.success(
        t('applications.spaceQuota.saved', { cluster: editing.cluster })
      )
      setEditing(null)
    } catch (error) {
      toast.error(translateError(error, t))
    }
  }

  const quotas = data?.quotas ?? []

  return (
    <Card>
      <CardHeader>
        <CardTitle>
          {t('applications.spaceQuota.title', { name: space })}
        </CardTitle>
        <CardDescription>
          {t('applications.spaceQuota.description')}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <p className="text-sm text-muted-foreground">
            {t('common.messages.loading')}
          </p>
        ) : quotas.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {t('applications.spaceQuota.empty')}
          </p>
        ) : (
          <div className="rounded-md border">
            <Table>
              <TableHeader className="bg-muted">
                <TableRow>
                  <TableHead>{t('applications.spaceQuota.cluster')}</TableHead>
                  <TableHead>{t('applications.spaceQuota.cpu')}</TableHead>
                  <TableHead>{t('applications.spaceQuota.memory')}</TableHead>
                  <TableHead className="w-24 text-right">
                    {t('common.fields.actions')}
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {quotas.map((quota) => (
                  <TableRow key={quota.cluster}>
                    <TableCell className="font-mono">{quota.cluster}</TableCell>
                    <UsageCells quota={quota} />
                    <TableCell className="text-right">
                      {quota.unconfigured ? (
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => setEditing(quota)}
                        >
                          {t('applications.spaceQuota.setQuota')}
                        </Button>
                      ) : (
                        !quota.unauthorized &&
                        !quota.error && (
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => setEditing(quota)}
                          >
                            {t('common.actions.edit')}
                          </Button>
                        )
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>

      <Dialog
        open={editing !== null}
        onOpenChange={(open) => {
          if (!open) setEditing(null)
        }}
      >
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>
              {t('applications.spaceQuota.editTitle', {
                cluster: editing?.cluster ?? '',
              })}
            </DialogTitle>
            <DialogDescription>
              {t('applications.spaceQuota.editHint', {
                cluster: editing?.cluster ?? '',
              })}
            </DialogDescription>
          </DialogHeader>
          {editing && (
            <div className="flex flex-wrap items-center gap-2">
              <span className="min-w-32 flex-1 truncate font-mono text-sm">
                {editing.cluster}
              </span>
              <ClusterQuotaFields
                cluster={editing.cluster}
                value={editValue}
                onChange={setEditValue}
              />
            </div>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setEditing(null)}>
              {t('common.actions.cancel')}
            </Button>
            <Button
              disabled={!quotaFieldValid(editValue) || upsertMutation.isPending}
              onClick={() => void save()}
            >
              {t('common.actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}
