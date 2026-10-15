// Editor toolbar dialogs: the ParamSpec table editor and the TriggerConfig
// editor (manual / webhook / cron / maxConcurrent). Both edit drafts locally
// and hand the result back on confirm.
import { useEffect, useState } from 'react'
import { IconPlus, IconTrash } from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
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
import { Switch } from '@/components/ui/switch'

import type { ParamSpec, ParamType, TriggerConfig } from '../types'

interface DialogShellProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  children: React.ReactNode
  onConfirm?: () => void
  confirmLabel?: string
  error?: string | null
  contentClassName?: string
}

function DialogShell({
  open,
  onOpenChange,
  title,
  children,
  onConfirm,
  confirmLabel,
  error,
  contentClassName,
}: DialogShellProps) {
  const { t } = useTranslation()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={contentClassName ?? 'max-w-2xl'}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <div className="max-h-[60vh] space-y-3 overflow-y-auto">{children}</div>
        {error && <p className="text-sm text-destructive">{error}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.actions.cancel')}
          </Button>
          {onConfirm && (
            <Button onClick={onConfirm}>
              {confirmLabel ?? t('common.actions.confirm')}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ── Params ──

const PARAM_TYPES: ParamType[] = ['string', 'number', 'bool', 'enum']

// One row per parameter: name | type | required | default | delete.
const PARAM_ROW_GRID =
  'grid grid-cols-[minmax(0,1fr)_120px_56px_minmax(0,1fr)_40px] items-center gap-2'

// One-click presets for common CI/CD parameters. All are string and optional:
// cron/webhook triggers then fall back to the default without validation.
const PARAM_PRESETS: {
  name: string
  default?: string
  descKey: string
}[] = [
  { name: 'gitRepo', descKey: 'workflow.params.presetGitRepoDesc' },
  {
    name: 'gitBranch',
    default: 'main',
    descKey: 'workflow.params.presetGitBranchDesc',
  },
  {
    name: 'dockerfile',
    default: 'Dockerfile',
    descKey: 'workflow.params.presetDockerfileDesc',
  },
  {
    name: 'imageTag',
    default: 'latest',
    descKey: 'workflow.params.presetImageTagDesc',
  },
]

export function ParamsDialog({
  open,
  onOpenChange,
  params,
  onSave,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  params: ParamSpec[]
  onSave: (params: ParamSpec[]) => void
}) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState<ParamSpec[]>([])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (open) {
      setDraft(params.map((p) => ({ ...p })))
      setError(null)
    }
  }, [open, params])

  const patch = (index: number, next: Partial<ParamSpec>) =>
    setDraft((prev) =>
      prev.map((p, i) => (i === index ? { ...p, ...next } : p))
    )

  const addPreset = (preset: (typeof PARAM_PRESETS)[number]) => {
    if (draft.some((p) => p.name.trim() === preset.name)) {
      toast.info(t('workflow.params.presetExists', { name: preset.name }))
      return
    }
    setDraft((prev) => [
      ...prev,
      {
        name: preset.name,
        type: 'string',
        required: false,
        default: preset.default,
      },
    ])
  }

  const confirm = () => {
    const names = new Set<string>()
    for (const p of draft) {
      if (p.name.trim() === '' || names.has(p.name.trim())) {
        setError(t('workflow.params.nameInvalid'))
        return
      }
      names.add(p.name.trim())
    }
    onSave(
      draft.map((p) => ({
        ...p,
        name: p.name.trim(),
        options: p.type === 'enum' ? splitOptions(p) : undefined,
      }))
    )
    onOpenChange(false)
  }

  return (
    <DialogShell
      open={open}
      onOpenChange={onOpenChange}
      title={t('workflow.params.title')}
      onConfirm={confirm}
      error={error}
      contentClassName="sm:max-w-4xl"
    >
      <p className="text-xs text-muted-foreground">
        {t('workflow.params.hint')}
      </p>
      <div className="space-y-2 rounded-lg border bg-muted/30 p-3">
        <div className="text-xs font-medium">
          {t('workflow.params.presetsTitle')}
        </div>
        <p className="text-[11px] text-muted-foreground">
          {t('workflow.params.presetsHint')}
        </p>
        <div className="flex flex-wrap gap-1.5">
          {PARAM_PRESETS.map((preset) => (
            <button
              key={preset.name}
              type="button"
              onClick={() => addPreset(preset)}
              className="flex items-center gap-1.5 rounded-md border bg-background px-2 py-1 text-xs transition-colors hover:bg-accent"
            >
              <span className="font-mono">{preset.name}</span>
              <span className="text-muted-foreground">{t(preset.descKey)}</span>
            </button>
          ))}
        </div>
      </div>
      {draft.length === 0 && (
        <p className="py-4 text-center text-sm text-muted-foreground">
          {t('workflow.params.empty')}
        </p>
      )}
      {draft.length > 0 && (
        <div className={`${PARAM_ROW_GRID} px-1`}>
          <Label className="text-xs">{t('workflow.params.name')}</Label>
          <Label className="text-xs">{t('workflow.params.type')}</Label>
          <Label className="text-xs">{t('workflow.params.required')}</Label>
          <Label className="text-xs">{t('workflow.params.defaultValue')}</Label>
          <span />
        </div>
      )}
      {draft.map((param, index) => (
        <div key={index} className="space-y-2 rounded-lg border p-3">
          <div className={PARAM_ROW_GRID}>
            <Input
              value={param.name}
              onChange={(e) => patch(index, { name: e.target.value })}
              className="font-mono"
            />
            <Select
              value={param.type}
              onValueChange={(v) =>
                patch(index, { type: v as ParamType, options: undefined })
              }
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PARAM_TYPES.map((type) => (
                  <SelectItem key={type} value={type}>
                    {type}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Checkbox
              checked={param.required === true}
              onCheckedChange={(checked) =>
                patch(index, { required: checked === true })
              }
              aria-label={t('workflow.params.required')}
              className="justify-self-center"
            />
            <Input
              value={
                param.default === undefined || param.default === null
                  ? ''
                  : String(param.default)
              }
              onChange={(e) =>
                patch(index, {
                  default: coerceDefault(e.target.value, param.type),
                })
              }
              className="h-8 font-mono text-xs"
            />
            <Button
              variant="ghost"
              size="icon"
              className="text-muted-foreground hover:text-destructive"
              onClick={() =>
                setDraft((prev) => prev.filter((_, i) => i !== index))
              }
            >
              <IconTrash className="h-4 w-4" />
            </Button>
          </div>
          {param.type === 'enum' && (
            <div className="space-y-1">
              <Label className="text-xs">{t('workflow.params.options')}</Label>
              <Input
                value={splitOptions(param).join(',')}
                onChange={(e) =>
                  patch(index, { options: e.target.value.split(',') })
                }
                placeholder="staging,production"
                className="font-mono text-xs"
              />
              <p className="text-[11px] text-muted-foreground">
                {t('workflow.params.optionsHint')}
              </p>
            </div>
          )}
        </div>
      ))}
      <Button
        variant="outline"
        size="sm"
        onClick={() =>
          setDraft((prev) => [
            ...prev,
            { name: '', type: 'string', required: false },
          ])
        }
      >
        <IconPlus className="mr-1.5 h-4 w-4" />
        {t('workflow.params.add')}
      </Button>
    </DialogShell>
  )
}

function splitOptions(param: ParamSpec): unknown[] {
  return (param.options ?? []).filter(
    (o) => o !== undefined && o !== null && String(o).trim() !== ''
  )
}

function coerceDefault(raw: string, type: ParamType): unknown {
  if (raw === '') return undefined
  if (type === 'number') {
    const n = Number(raw)
    return Number.isNaN(n) ? raw : n
  }
  if (type === 'bool') return raw === 'true'
  return raw
}

// ── Triggers ──

export function TriggersDialog({
  open,
  onOpenChange,
  triggers,
  onSave,
  onManageToken,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  triggers: TriggerConfig
  onSave: (triggers: TriggerConfig) => void
  onManageToken?: () => void
}) {
  const { t } = useTranslation()
  const [manual, setManual] = useState(true)
  const [webhook, setWebhook] = useState(false)
  const [cron, setCron] = useState('')
  const [maxConcurrent, setMaxConcurrent] = useState('')

  useEffect(() => {
    if (open) {
      setManual(triggers.manual !== false)
      setWebhook(triggers.webhook === true)
      setCron(triggers.cron ?? '')
      setMaxConcurrent(
        triggers.maxConcurrent ? String(triggers.maxConcurrent) : ''
      )
    }
  }, [open, triggers])

  const confirm = () => {
    onSave({
      manual: manual ? undefined : false,
      webhook: webhook || undefined,
      cron: cron.trim() || undefined,
      maxConcurrent: maxConcurrent.trim()
        ? Number(maxConcurrent.trim())
        : undefined,
    })
    onOpenChange(false)
  }

  return (
    <DialogShell
      open={open}
      onOpenChange={onOpenChange}
      title={t('workflow.triggers.title')}
      onConfirm={confirm}
    >
      <TriggerRow
        title={t('workflow.triggers.manual')}
        description={t('workflow.triggers.manualDesc')}
        checked={manual}
        onCheckedChange={setManual}
      />
      <TriggerRow
        title={t('workflow.triggers.webhook')}
        description={t('workflow.triggers.webhookDesc')}
        checked={webhook}
        onCheckedChange={setWebhook}
        action={
          onManageToken && (
            <Button variant="outline" size="sm" onClick={onManageToken}>
              {t('workflow.triggers.manageToken')}
            </Button>
          )
        }
      />
      <div className="flex items-center justify-between rounded-lg border px-3 py-2.5">
        <div>
          <div className="text-sm font-medium">
            {t('workflow.triggers.cron')}
          </div>
          <div className="text-xs text-muted-foreground">
            {t('workflow.triggers.cronDesc')}
          </div>
        </div>
        <Input
          value={cron}
          onChange={(e) => setCron(e.target.value)}
          placeholder="30 2 * * *"
          className="h-8 w-40 font-mono text-xs"
        />
      </div>
      <div className="flex items-center justify-between rounded-lg border px-3 py-2.5">
        <div>
          <div className="text-sm font-medium">
            {t('workflow.triggers.maxConcurrent')}
          </div>
          <div className="text-xs text-muted-foreground">
            {t('workflow.triggers.maxConcurrentHint')}
          </div>
        </div>
        <Input
          value={maxConcurrent}
          onChange={(e) => setMaxConcurrent(e.target.value.replace(/\D/g, ''))}
          className="h-8 w-20 text-right font-mono text-xs"
          placeholder="3"
        />
      </div>
    </DialogShell>
  )
}

function TriggerRow({
  title,
  description,
  checked,
  onCheckedChange,
  action,
}: {
  title: string
  description: string
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  action?: React.ReactNode
}) {
  return (
    <div className="flex items-center justify-between rounded-lg border px-3 py-2.5">
      <div>
        <div className="text-sm font-medium">{title}</div>
        <div className="text-xs text-muted-foreground">{description}</div>
      </div>
      <div className="flex items-center gap-2">
        {action}
        <Switch checked={checked} onCheckedChange={onCheckedChange} />
      </div>
    </div>
  )
}
