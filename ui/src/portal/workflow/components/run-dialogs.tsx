// Manual-trigger dialog: renders a form from the workflow's ParamSpec schema
// (design §11 行内「运行」), plus the webhook token view/copy/reset dialog
// backed by GET/POST /workflows/:id/webhook-token(/reset). The workflow is
// addressed by its numeric ID; the name is display only.
import { useEffect, useState } from 'react'
import { IconCopy, IconRefresh } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { withSubPath } from '@/lib/subpath'
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
import { Skeleton } from '@/components/ui/skeleton'

import {
  fetchWebhookToken,
  resetWebhookToken,
  useTriggerRunMutation,
  useWorkflow,
} from '../api'
import type { ParamSpec, TriggerRunResponse } from '../types'

export function RunParamsDialog({
  id,
  name,
  params,
  open,
  onOpenChange,
  onTriggered,
}: {
  id: number
  name: string
  params: ParamSpec[]
  open: boolean
  onOpenChange: (open: boolean) => void
  onTriggered: (run: TriggerRunResponse) => void
}) {
  const { t } = useTranslation()
  const [values, setValues] = useState<Record<string, string>>({})

  useEffect(() => {
    if (open) {
      const defaults: Record<string, string> = {}
      for (const p of params) {
        defaults[p.name] =
          p.default === undefined || p.default === null ? '' : String(p.default)
      }
      setValues(defaults)
    }
  }, [open, params])

  const trigger = useTriggerRunMutation(id)

  const submit = () => {
    const payload: Record<string, unknown> = {}
    for (const p of params) {
      const raw = values[p.name] ?? ''
      if (raw === '') {
        if (p.required) {
          toast.error(t('workflow.list.invalidParam', { name: p.name }))
          return
        }
        continue
      }
      if (p.type === 'number') {
        const n = Number(raw)
        if (Number.isNaN(n)) {
          toast.error(t('workflow.list.invalidParam', { name: p.name }))
          return
        }
        payload[p.name] = n
        continue
      }
      if (p.type === 'bool') {
        payload[p.name] = raw === 'true'
        continue
      }
      payload[p.name] = raw
    }
    trigger.mutate(payload, {
      onSuccess: (run) => {
        toast.success(t('workflow.list.started', { n: run.runNumber }))
        onOpenChange(false)
        onTriggered(run)
      },
      onError: (err) => toast.error(err.message),
    })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>
            {t('workflow.list.runParamsTitle', { name })}
          </DialogTitle>
          <DialogDescription>
            {t('workflow.list.runParamsHint')}
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-[50vh] space-y-3 overflow-y-auto">
          {params.length === 0 && (
            <p className="text-sm text-muted-foreground">
              {t('workflow.list.noParams')}
            </p>
          )}
          {params.map((param) => (
            <div key={param.name} className="space-y-1.5">
              <Label className="flex items-center gap-1.5 font-mono text-xs">
                {param.name}
                {param.required && <span className="text-destructive">*</span>}
                <span className="text-[10px] font-normal text-muted-foreground">
                  {param.type}
                </span>
              </Label>
              {param.type === 'bool' ? (
                <Select
                  value={values[param.name] ?? 'false'}
                  onValueChange={(v) =>
                    setValues((prev) => ({ ...prev, [param.name]: v }))
                  }
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="false">false</SelectItem>
                    <SelectItem value="true">true</SelectItem>
                  </SelectContent>
                </Select>
              ) : param.type === 'enum' ? (
                <Select
                  value={values[param.name] ?? ''}
                  onValueChange={(v) =>
                    setValues((prev) => ({ ...prev, [param.name]: v }))
                  }
                >
                  <SelectTrigger className="w-full">
                    <SelectValue placeholder={param.name} />
                  </SelectTrigger>
                  <SelectContent>
                    {(param.options ?? []).map((option) => (
                      <SelectItem key={String(option)} value={String(option)}>
                        {String(option)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : (
                <Input
                  value={values[param.name] ?? ''}
                  type={param.type === 'number' ? 'number' : 'text'}
                  onChange={(e) =>
                    setValues((prev) => ({
                      ...prev,
                      [param.name]: e.target.value,
                    }))
                  }
                  className="font-mono"
                />
              )}
            </div>
          ))}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.actions.cancel')}
          </Button>
          <Button disabled={trigger.isPending} onClick={submit}>
            ▶ {t('workflow.list.run')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// Dialog host for inline run triggers from list surfaces (workflow list,
// application detail's related-workflows hub): the list API omits the params
// schema, so the host loads the workflow detail first and opens once the
// params are known.
export function RunParamsDialogHost({
  id,
  name,
  onOpenChange,
  onTriggered,
}: {
  id: number
  name: string
  onOpenChange: (open: boolean) => void
  onTriggered: (run: TriggerRunResponse) => void
}) {
  const { data: workflow } = useWorkflow(id)
  return (
    <RunParamsDialog
      id={id}
      name={name}
      params={workflow?.params ?? []}
      open={workflow !== undefined}
      onOpenChange={onOpenChange}
      onTriggered={onTriggered}
    />
  )
}

export function WebhookTokenDialog({
  id,
  open,
  onOpenChange,
}: {
  id: number
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['workflow', 'webhook-token', id],
    queryFn: () => fetchWebhookToken(id),
    enabled: open,
  })

  // Feed the reset response straight into the token query so the dialog never
  // keeps showing the invalidated old value past the 5-minute staleTime.
  const reset = useMutation({
    mutationFn: () => resetWebhookToken(id),
    onSuccess: (data) =>
      queryClient.setQueryData(['workflow', 'webhook-token', id], data),
  })

  const webhookUrl = data?.token
    ? `${window.location.origin}${withSubPath(`/api/workflow-hooks/${data.token}`)}`
    : ''

  const copy = async (value: string) => {
    await navigator.clipboard.writeText(value)
    toast.success(t('workflow.list.copied'))
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* minmax(0,1fr) keeps the grid column at the dialog width — the
          unbreakable URL/Token strings otherwise size the auto track to
          max-content and push the Copy buttons out of the viewport. */}
      <DialogContent className="grid-cols-[minmax(0,1fr)]">
        <DialogHeader>
          <DialogTitle>{t('workflow.list.webhookToken')}</DialogTitle>
          <DialogDescription>
            {t('workflow.list.webhookHint')}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div className="space-y-1.5">
            <Label className="text-xs">{t('workflow.list.webhookUrl')}</Label>
            {isLoading ? (
              <Skeleton className="h-9 w-full" />
            ) : (
              <div className="flex min-w-0 items-center gap-2">
                <code className="min-w-0 flex-1 truncate rounded bg-muted px-2 py-2 font-mono text-xs">
                  {webhookUrl}
                </code>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => void copy(webhookUrl)}
                >
                  <IconCopy className="mr-1 h-3.5 w-3.5" />
                  {t('workflow.list.copy')}
                </Button>
              </div>
            )}
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">{t('workflow.list.tokenLabel')}</Label>
            <div className="flex min-w-0 items-center gap-2">
              <code className="min-w-0 flex-1 truncate rounded bg-muted px-2 py-2 font-mono text-xs">
                {data?.token ?? ''}
              </code>
              <Button
                variant="outline"
                size="sm"
                onClick={() => data && void copy(data.token)}
              >
                <IconCopy className="mr-1 h-3.5 w-3.5" />
                {t('workflow.list.copy')}
              </Button>
            </div>
          </div>
          <div className="flex items-center justify-between gap-2">
            <p className="min-w-0 text-xs text-muted-foreground">
              {t('workflow.list.resetTokenConfirm')}
            </p>
            <Button
              variant="outline"
              size="sm"
              disabled={reset.isPending}
              onClick={() =>
                reset.mutate(undefined, {
                  onSuccess: () => toast.success(t('workflow.list.tokenReset')),
                  onError: (err) => toast.error(err.message),
                })
              }
            >
              <IconRefresh className="mr-1.5 h-3.5 w-3.5" />
              {t('workflow.list.resetToken')}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
