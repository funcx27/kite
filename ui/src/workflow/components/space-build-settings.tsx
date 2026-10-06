// Per-space build settings: the build provider default of one space. Saving
// upserts the space's build-settings row; "follow global" deletes it,
// letting the global default row apply again. The settings page passes the
// space from its ?space= URL param and renders this section only with one.
import '@/workflow/i18n'

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

import {
  useBuildSettings,
  useDeleteBuildSettingMutation,
  useUpsertBuildSettingMutation,
} from '../api'

// Select sentinel for "follow the global default row".
const FOLLOW_GLOBAL = '__global__'

export function SpaceBuildSettings({ space }: { space: string | null }) {
  const { t } = useTranslation()
  const { data, isLoading } = useBuildSettings()
  const upsertMutation = useUpsertBuildSettingMutation()
  const deleteMutation = useDeleteBuildSettingMutation()

  const settings = useMemo(() => data?.settings ?? [], [data])
  const spaceRow = useMemo(
    () => settings.find((s) => s.namespace === space),
    [settings, space]
  )

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

  const value = selected ?? spaceRow?.provider ?? FOLLOW_GLOBAL
  const unchanged = value === (spaceRow?.provider ?? FOLLOW_GLOBAL)

  const save = () => {
    if (!space || unchanged) return
    const onError = (err: Error) => toast.error(err.message)
    if (value === FOLLOW_GLOBAL) {
      deleteMutation.mutate(space, {
        onSuccess: () => toast.success(t('workflow.spaceSettings.saved')),
        onError,
      })
      return
    }
    upsertMutation.mutate(
      { namespace: space, provider: value },
      {
        onSuccess: () => toast.success(t('workflow.spaceSettings.saved')),
        onError,
      }
    )
  }

  const pending = upsertMutation.isPending || deleteMutation.isPending

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
                  <SelectItem value={FOLLOW_GLOBAL}>
                    {t('workflow.spaceSettings.followGlobal')}
                  </SelectItem>
                  {(data?.providers ?? []).map((provider) => (
                    <SelectItem key={provider} value={provider}>
                      {provider}
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
