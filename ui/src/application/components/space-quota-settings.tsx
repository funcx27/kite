// Per-space resource quotas: one row per cluster that reports the space
// (namespace exists there). Unconfigured clusters get a "set quota" action,
// configured ones show the quota against the cluster's allocatable total
// ("4/10 core") and the quota-observed usage against the quota; editing
// opens a dialog with the same core/GiB inputs. The upsert is idempotent per
// cluster, so a failed save keeps the dialog open with the entered values.
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
import { Label } from '@/components/ui/label'
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
  withLabels = false,
}: {
  cluster: string
  value: QuotaFieldValue
  onChange: (next: QuotaFieldValue) => void
  disabled?: boolean
  withLabels?: boolean
}) {
  const { t } = useTranslation()
  const { data } = useClusterCapacity(cluster)
  const hasCapacity = !!data && data.nodes > 0
  const cpuSuffix = hasCapacity
    ? `/ ${formatCores(data.cpuCores)} core`
    : 'core'
  const memSuffix = hasCapacity ? `/ ${formatGiB(data.memoryGiB)} GiB` : 'GiB'
  const suffixField = (
    key: 'cpu' | 'memory',
    label: string,
    suffix: string
  ) => (
    <div className="min-w-0 flex-1">
      {withLabels && <Label className="text-xs">{label}</Label>}
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
      {suffixField('cpu', t('applications.spaceQuota.cpu'), cpuSuffix)}
      {suffixField('memory', t('applications.spaceQuota.memory'), memSuffix)}
    </div>
  )
}

export interface QuotaDenominator {
  cpuCores?: number
  memoryGiB?: number
}

// "x/y core · x/y GiB" against an optional denominator; a missing denominator
// or unparseable value degrades that side to the plain quantity.
export function ratioPair(
  cpuQ: string | undefined,
  memoryQ: string | undefined,
  denom?: QuotaDenominator,
  className = 'font-mono'
): React.ReactNode {
  const cores = cpuQuantityToCores(cpuQ)
  const gib = memoryQuantityToGiB(memoryQ)
  if (cores === undefined && gib === undefined) {
    return (
      <span className={`${className} text-muted-foreground`}>
        {cpuQ || memoryQ || '—'}
      </span>
    )
  }
  const side = (
    value: number | undefined,
    denomValue: number | undefined,
    unit: string
  ) => {
    if (value === undefined) return null
    return denomValue === undefined
      ? `${formatCores(value)} ${unit}`
      : `${formatCores(value)}/${formatCores(denomValue)} ${unit}`
  }
  const parts = [
    side(cores, denom?.cpuCores, 'core'),
    side(gib, denom?.memoryGiB, 'GiB'),
  ].filter((part): part is string => part !== null)
  return <span className={className}>{parts.join(' · ')}</span>
}

// Quota cell of the settings table: hard limits against the cluster's
// allocatable total, amber when the quota overshoots that capacity.
function QuotaCell({ quota }: { quota: SpaceQuotaStatus }) {
  const { t } = useTranslation()
  const { data } = useClusterCapacity(quota.cluster)
  if (quota.unauthorized) {
    return (
      <span className="text-muted-foreground">
        {t('applications.spaceQuota.unauthorized')}
      </span>
    )
  }
  if (quota.error) {
    return <span className="text-destructive">{quota.error}</span>
  }
  if (quota.unconfigured) {
    return (
      <span className="text-muted-foreground">
        {t('applications.spaceQuota.unconfigured')}
      </span>
    )
  }
  const denom =
    data && data.nodes > 0
      ? { cpuCores: data.cpuCores, memoryGiB: data.memoryGiB }
      : undefined
  const over =
    denom !== undefined &&
    ((cpuQuantityToCores(quota.cpu) ?? 0) > denom.cpuCores ||
      (memoryQuantityToGiB(quota.memory) ?? 0) > denom.memoryGiB)
  return (
    <span
      className={
        over ? 'font-mono text-amber-600 dark:text-amber-400' : undefined
      }
      title={over ? t('applications.spaceQuota.overCapacity') : undefined}
    >
      {ratioPair(quota.cpu, quota.memory, denom, over ? '' : 'font-mono')}
    </span>
  )
}

// Usage cell: the quota-observed usage against the quota itself.
function UsedCell({ quota }: { quota: SpaceQuotaStatus }) {
  if (quota.unconfigured || quota.unauthorized || quota.error) return null
  return ratioPair(
    quota.usedCpu,
    quota.usedMemory,
    {
      cpuCores: cpuQuantityToCores(quota.cpu),
      memoryGiB: memoryQuantityToGiB(quota.memory),
    },
    'font-mono text-muted-foreground'
  )
}

// Read-only "quota / cluster total" pair for clusters whose space already
// carries a quota (create-space dialog).
export function ConfiguredQuota({ quota }: { quota: SpaceQuotaStatus }) {
  const { data } = useClusterCapacity(quota.cluster)
  const denom =
    data && data.nodes > 0
      ? { cpuCores: data.cpuCores, memoryGiB: data.memoryGiB }
      : undefined
  return (
    <div className="flex flex-1 justify-end">
      {ratioPair(
        quota.cpu,
        quota.memory,
        denom,
        'font-mono text-sm text-muted-foreground'
      )}
    </div>
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
                  <TableHead>{t('applications.spaceQuota.quota')}</TableHead>
                  <TableHead>{t('applications.spaceQuota.used')}</TableHead>
                  <TableHead className="w-24 text-right">
                    {t('common.fields.actions')}
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {quotas.map((quota) => (
                  <TableRow key={quota.cluster}>
                    <TableCell className="font-mono">{quota.cluster}</TableCell>
                    <TableCell>
                      <QuotaCell quota={quota} />
                    </TableCell>
                    <TableCell>
                      <UsedCell quota={quota} />
                    </TableCell>
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
        <DialogContent className="sm:max-w-sm">
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
            <ClusterQuotaFields
              cluster={editing.cluster}
              value={editValue}
              onChange={setEditValue}
              withLabels
            />
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
