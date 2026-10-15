// Per-space build settings: the build provider of one space. The select lists
// every provider with the global default marked; saving upserts the space's
// build-settings row (a space without a row shows the global default). The
// settings page passes the space from its ?space= URL param.
import '@/portal/workflow/i18n'

import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { useBuildSettings, useUpsertBuildSettingMutation } from '../api'

export function SpaceBuildSettings({ space }: { space: string | null }) {
  const { t } = useTranslation()
  const { data, isLoading } = useBuildSettings()
  const upsertMutation = useUpsertBuildSettingMutation()

  const settings = useMemo(() => data?.settings ?? [], [data])
  const spaceRow = useMemo(
    () => settings.find((s) => s.namespace === space),
    [settings, space]
  )
  // Global default row ('' namespace); marks the default option and seeds the
  // select when the space has no explicit row yet.
  const globalProvider =
    settings.find((s) => s.namespace === '')?.provider ?? ''

  // null = untouched, so the select tracks the stored row until the user
  // actively picks a provider; resets when the selected space changes.
  const [selected, setSelected] = useState<string | null>(null)
  useEffect(() => {
    setSelected(null)
  }, [space])

  if (!space) {
    return (
      <p className="text-sm text-muted-foreground">
        {t('workflow.spaceSettings.noSpace')}
      </p>
    )
  }

  const value = selected ?? spaceRow?.provider ?? globalProvider
  const unchanged = value === (spaceRow?.provider ?? globalProvider)

  const save = () => {
    if (!space || unchanged) return
    upsertMutation.mutate(
      { namespace: space, provider: value },
      {
        onSuccess: () => toast.success(t('workflow.spaceSettings.saved')),
        onError: (err: Error) => toast.error(err.message),
      }
    )
  }

  const pending = upsertMutation.isPending

  return (
    <Card>
      <CardHeader>
        <CardTitle>
          {t('workflow.spaceSettings.title', { name: space })}
        </CardTitle>
        <CardDescription>
          {t('workflow.spaceSettings.providerHint')}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <p className="text-sm text-muted-foreground">
            {t('common.messages.loading')}
          </p>
        ) : (
          <div className="flex max-w-md flex-col gap-1.5">
            <Label className="text-xs">
              {t('workflow.spaceSettings.providerLabel')}
            </Label>
            <div className="flex items-center gap-2">
              <Select value={value} onValueChange={setSelected}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(data?.providers ?? []).map((provider) => (
                    <SelectItem key={provider} value={provider}>
                      {provider === globalProvider
                        ? t('workflow.spaceSettings.providerWithDefault', {
                            name: provider,
                          })
                        : provider}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Button
                size="sm"
                variant="outline"
                disabled={pending || unchanged}
                onClick={save}
              >
                {t('common.actions.save')}
              </Button>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
