// Frontend mock: subscription rules (event source × delivery channels) live
// in local state only and are never persisted.
import { useRef, useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
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

type EventSource =
  | 'pipeline'
  | 'approval'
  | 'appNotReady'
  | 'configDrift'
  | 'certExpiring'
  | 'restartStorm'

const EVENT_SOURCES: EventSource[] = [
  'pipeline',
  'approval',
  'appNotReady',
  'configDrift',
  'certExpiring',
  'restartStorm',
]

interface SubscriptionRule {
  id: string
  source: EventSource
  inApp: boolean
  webhook: boolean
  webhookUrl?: string
}

const INITIAL_RULES: SubscriptionRule[] = [
  {
    id: 'rule-pipeline',
    source: 'pipeline',
    inApp: true,
    webhook: true,
    webhookUrl: 'https://example.com/hooks/kite',
  },
  {
    id: 'rule-drift',
    source: 'configDrift',
    inApp: true,
    webhook: false,
  },
  {
    id: 'rule-cert',
    source: 'certExpiring',
    inApp: true,
    webhook: true,
    webhookUrl: 'https://hooks.example.com/kite-alerts',
  },
]

function RuleDialog({
  open,
  onOpenChange,
  onCreate,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreate: (rule: Omit<SubscriptionRule, 'id'>) => void
}) {
  const { t } = useTranslation()
  const [source, setSource] = useState<EventSource>('pipeline')
  const [inApp, setInApp] = useState(true)
  const [webhook, setWebhook] = useState(false)
  const [webhookUrl, setWebhookUrl] = useState('')

  const valid = (inApp || webhook) && (!webhook || webhookUrl.trim() !== '')

  const submit = () => {
    if (!valid) return
    onCreate({
      source,
      inApp,
      webhook,
      webhookUrl: webhook ? webhookUrl.trim() : undefined,
    })
    setSource('pipeline')
    setInApp(true)
    setWebhook(false)
    setWebhookUrl('')
    onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t('notifications.rules.dialog.title')}</DialogTitle>
          <DialogDescription>
            {t('notifications.rules.dialog.description')}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div className="space-y-1.5">
            <Label>{t('notifications.rules.dialog.source')}</Label>
            <Select
              value={source}
              onValueChange={(value) => setSource(value as EventSource)}
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {EVENT_SOURCES.map((candidate) => (
                  <SelectItem key={candidate} value={candidate}>
                    {t(`notifications.source.${candidate}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label>{t('notifications.rules.dialog.channels')}</Label>
            <div className="flex items-center gap-2">
              <Checkbox
                id="rule-in-app"
                checked={inApp}
                onCheckedChange={(checked) => setInApp(checked === true)}
              />
              <Label htmlFor="rule-in-app" className="font-normal">
                {t('notifications.channels.inApp')}
              </Label>
            </div>
            <div className="flex items-center gap-2">
              <Checkbox
                id="rule-webhook"
                checked={webhook}
                onCheckedChange={(checked) => setWebhook(checked === true)}
              />
              <Label htmlFor="rule-webhook" className="font-normal">
                {t('notifications.channels.webhook')}
              </Label>
            </div>
            {webhook && (
              <Input
                value={webhookUrl}
                onChange={(event) => setWebhookUrl(event.target.value)}
                placeholder="https://example.com/hooks/kite"
                aria-label={t('notifications.rules.dialog.webhookUrl')}
                className="font-mono"
              />
            )}
          </div>
        </div>
        <DialogFooter>
          {!valid && (
            <p className="mr-auto text-xs text-destructive">
              {t('notifications.rules.dialog.invalid')}
            </p>
          )}
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('notifications.rules.dialog.cancel')}
          </Button>
          <Button disabled={!valid} onClick={submit}>
            {t('notifications.rules.dialog.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function SubscriptionRules() {
  const { t } = useTranslation()
  const [rules, setRules] = useState<SubscriptionRule[]>(INITIAL_RULES)
  const [dialogOpen, setDialogOpen] = useState(false)
  const ruleSeqRef = useRef(0)

  const removeRule = (id: string) => {
    setRules((prev) => prev.filter((rule) => rule.id !== id))
    toast.success(t('notifications.rules.toast.deleted'))
  }

  const createRule = (input: Omit<SubscriptionRule, 'id'>) => {
    ruleSeqRef.current += 1
    setRules((prev) => [
      ...prev,
      { ...input, id: `rule-${ruleSeqRef.current}-${input.source}` },
    ])
    toast.success(t('notifications.rules.toast.created'))
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-end">
        <Button onClick={() => setDialogOpen(true)}>
          <Plus className="h-3.5 w-3.5" />
          {t('notifications.rules.new')}
        </Button>
      </div>
      {rules.length === 0 ? (
        <div className="flex flex-col items-center gap-1 py-12 text-center">
          <p className="text-sm font-medium text-muted-foreground">
            {t('notifications.rules.emptyTitle')}
          </p>
          <p className="text-sm text-muted-foreground">
            {t('notifications.rules.emptyDescription')}
          </p>
        </div>
      ) : (
        <Card>
          <CardContent className="divide-y p-0">
            {rules.map((rule) => (
              <div
                key={rule.id}
                className="flex flex-wrap items-center justify-between gap-3 px-4 py-3"
              >
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <span className="text-sm font-medium">
                    {t(`notifications.source.${rule.source}`)}
                  </span>
                  <Badge variant="outline">
                    {t('notifications.rules.scopeAllSpaces')}
                  </Badge>
                  {rule.inApp && (
                    <Badge variant="secondary">
                      {t('notifications.channels.inApp')}
                    </Badge>
                  )}
                  {rule.webhook && (
                    <Badge variant="outline">
                      {t('notifications.channels.webhook')}
                    </Badge>
                  )}
                  {rule.webhookUrl && (
                    <span className="truncate font-mono text-xs text-muted-foreground">
                      {rule.webhookUrl}
                    </span>
                  )}
                </div>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7"
                  aria-label={t('notifications.rules.delete')}
                  onClick={() => removeRule(rule.id)}
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </div>
            ))}
          </CardContent>
        </Card>
      )}
      <RuleDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onCreate={createRule}
      />
    </div>
  )
}
