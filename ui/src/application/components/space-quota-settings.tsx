// Per-space resource quotas: one row per cluster that reports the space
// (namespace exists there). Unconfigured clusters get a "set quota" action,
// configured ones show the hard limits and the quota-observed usage; editing
// opens a dialog with the same core/GiB inputs and capacity reference line
// as the create-space dialog. The upsert is idempotent per cluster, so a
// failed save keeps the dialog open with the entered values for retry.
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

// The allocatable-capacity reference line; hidden whenever the cluster's
// nodes cannot be read (missing permission, unreachable) so it never blocks
// quota entry.
function ClusterCapacityLine({ cluster }: { cluster: string }) {
  const { t } = useTranslation()
  const { data } = useClusterCapacity(cluster)
  if (!data || data.nodes === 0) return null
  return (
    <p className="text-xs text-muted-foreground">
      {t('applications.spaceQuota.capacityHint', {
        cpu: formatCores(data.cpuCores),
        memory: formatGiB(data.memoryGiB),
        nodes: data.nodes,
      })}
    </p>
  )
}

// Shared per-cluster quota editor: plain-number inputs with a fixed core /
// GiB unit suffix plus the capacity reference line.
export function ClusterQuotaFields({
  cluster,
  value,
  onChange,
}: {
  cluster: string
  value: QuotaFieldValue
  onChange: (next: QuotaFieldValue) => void
}) {
  const { t } = useTranslation()
  const suffixField = (key: 'cpu' | 'memory', label: string, unit: string) => (
    <div className="flex-1">
      <Label className="text-xs">{label}</Label>
      <div className="relative mt-1">
        <Input
          type="number"
          min={0}
          step="any"
          className="pr-12"
          value={value[key]}
          onChange={(event) =>
            onChange({ ...value, [key]: event.target.value })
          }
          placeholder="0"
        />
        <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs text-muted-foreground">
          {unit}
        </span>
      </div>
    </div>
  )
  return (
    <div className="space-y-1.5">
      <div className="flex gap-2">
        {suffixField('cpu', t('applications.spaceQuota.cpu'), 'core')}
        {suffixField('memory', t('applications.spaceQuota.memory'), 'GiB')}
      </div>
      <ClusterCapacityLine cluster={cluster} />
    </div>
  )
}

// "X core / Y GiB" from two quantity strings; unparseable values fall back
// to echoing the raw string.
function resourcePair(
  cpuQ: string | undefined,
  memoryQ: string | undefined
): React.ReactNode {
  const cores = cpuQuantityToCores(cpuQ)
  const gib = memoryQuantityToGiB(memoryQ)
  if (cores === undefined && gib === undefined) {
    return (
      <span className="font-mono text-muted-foreground">
        {cpuQ || memoryQ || '—'}
      </span>
    )
  }
  return (
    <span className="font-mono">
      {formatCores(cores ?? 0)} core / {formatGiB(gib ?? 0)} GiB
    </span>
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

  const renderQuota = (quota: SpaceQuotaStatus) => {
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
    return resourcePair(quota.cpu, quota.memory)
  }

  const renderUsed = (quota: SpaceQuotaStatus) => {
    if (quota.unconfigured || quota.unauthorized || quota.error) return null
    return resourcePair(quota.usedCpu, quota.usedMemory)
  }

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
                    <TableCell>{renderQuota(quota)}</TableCell>
                    <TableCell>{renderUsed(quota)}</TableCell>
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
