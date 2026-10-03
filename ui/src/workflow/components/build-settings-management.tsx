// Build cluster settings (workflow-design build settings): one default build
// provider per namespace, plus the global default row (namespace '').
// Provider choices come from the buildfleet config; without any, the provider
// falls back to a plain text input.
import '@/workflow/i18n'

import { useMemo, useState } from 'react'
import { IconPlus, IconTrash } from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
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
  useBuildSettings,
  useDeleteBuildSettingMutation,
  useUpsertBuildSettingMutation,
} from '../api'
import type { BuildSetting } from '../types'

const dns1123LabelPattern = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

// One row being added or edited; original is null while adding.
interface DraftRow {
  namespace: string
  provider: string
  isNew: boolean
  original: BuildSetting | null
}

export function BuildSettingsManagement() {
  const { t } = useTranslation()

  const { data, isLoading } = useBuildSettings()
  const settings = data?.settings ?? []
  const providers = data?.providers ?? []
  const upsertMutation = useUpsertBuildSettingMutation()
  const deleteMutation = useDeleteBuildSettingMutation()

  const [draft, setDraft] = useState<DraftRow | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<BuildSetting | null>(null)

  const openAdd = () =>
    setDraft({
      namespace: '',
      provider: providers[0] ?? '',
      isNew: true,
      original: null,
    })

  const openEdit = (setting: BuildSetting) =>
    setDraft({
      namespace: setting.namespace,
      provider: setting.provider,
      isNew: false,
      original: setting,
    })

  // Select mode keeps a stored value visible even when it has since been
  // dropped from the buildfleet config.
  const providerOptions = useMemo(() => {
    if (providers.length === 0) return []
    if (draft && draft.provider !== '' && !providers.includes(draft.provider)) {
      return [...providers, draft.provider].sort()
    }
    return providers
  }, [providers, draft])

  const save = () => {
    if (!draft) return
    const namespace = draft.namespace.trim()
    const provider = draft.provider.trim()
    if (
      namespace !== '' &&
      (!dns1123LabelPattern.test(namespace) || namespace.length > 63)
    ) {
      toast.error(t('workflow.buildSettings.namespaceInvalid'))
      return
    }
    if (provider === '') {
      toast.error(t('workflow.buildSettings.providerRequired'))
      return
    }
    if (
      settings.some(
        (s) =>
          s.namespace === namespace && s.namespace !== draft.original?.namespace
      )
    ) {
      toast.error(t('workflow.buildSettings.namespaceDuplicate'))
      return
    }
    const finish = () => {
      toast.success(t('workflow.buildSettings.saved'))
      setDraft(null)
    }
    upsertMutation.mutate(
      { namespace, provider },
      {
        onSuccess: finish,
        onError: (err) => toast.error(err.message),
      }
    )
  }

  const pending = upsertMutation.isPending || deleteMutation.isPending

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between space-y-0">
        <div className="space-y-1">
          <CardTitle>{t('workflow.buildSettings.title')}</CardTitle>
          <CardDescription>
            {t('workflow.buildSettings.description')}
          </CardDescription>
        </div>
        <Button onClick={openAdd} disabled={draft !== null}>
          <IconPlus className="mr-1.5 h-4 w-4" />
          {t('workflow.buildSettings.add')}
        </Button>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('workflow.buildSettings.namespace')}</TableHead>
              <TableHead>{t('workflow.buildSettings.provider')}</TableHead>
              <TableHead className="text-right">
                {t('common.fields.actions')}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {draft?.isNew && (
              <TableRow>
                <BuildSettingEditCells
                  draft={draft}
                  providerOptions={providerOptions}
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
            {settings.map((setting) =>
              draft?.original?.namespace === setting.namespace ? (
                <TableRow key={setting.namespace}>
                  <BuildSettingEditCells
                    draft={draft}
                    providerOptions={providerOptions}
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
              ) : (
                <TableRow key={setting.namespace}>
                  <TableCell>
                    {setting.namespace === '' ? (
                      <div className="space-y-1">
                        <Badge variant="secondary">
                          {t('workflow.buildSettings.globalDefault')}
                        </Badge>
                        <p className="text-[11px] text-muted-foreground">
                          {t('workflow.buildSettings.globalDefaultHint')}
                        </p>
                      </div>
                    ) : (
                      <span className="font-mono">{setting.namespace}</span>
                    )}
                  </TableCell>
                  <TableCell>
                    <span className="font-mono">{setting.provider}</span>
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1.5">
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={draft !== null}
                        onClick={() => openEdit(setting)}
                      >
                        {t('common.actions.edit')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="text-muted-foreground hover:text-destructive"
                        disabled={draft !== null}
                        onClick={() => setDeleteTarget(setting)}
                      >
                        <IconTrash className="h-4 w-4" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              )
            )}
          </TableBody>
        </Table>
        {isLoading && (
          <p className="py-4 text-center text-sm text-muted-foreground">
            {t('common.messages.loading')}
          </p>
        )}
        {!isLoading && settings.length === 0 && !draft?.isNew && (
          <p className="py-4 text-center text-sm text-muted-foreground">
            {t('workflow.buildSettings.empty')}
          </p>
        )}
      </CardContent>

      <DeleteConfirmationDialog
        open={deleteTarget !== null}
        onOpenChange={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (!deleteTarget) return
          deleteMutation.mutate(deleteTarget.namespace, {
            onSuccess: () => {
              toast.success(t('workflow.buildSettings.deleted'))
              setDeleteTarget(null)
            },
            onError: (err) => toast.error(err.message),
          })
        }}
        resourceName={
          deleteTarget?.namespace === ''
            ? t('workflow.buildSettings.globalDefault')
            : (deleteTarget?.namespace ?? '')
        }
        resourceType={t('workflow.buildSettings.deleteType')}
        additionalNote={t('workflow.buildSettings.deleteDescription')}
      />
    </Card>
  )
}

// BuildSettingEditCells renders the namespace/provider inputs of a draft row
// (new or existing): the provider is a dropdown over the configured
// providers, falling back to a text input when none are available.
function BuildSettingEditCells({
  draft,
  providerOptions,
  onChange,
}: {
  draft: DraftRow
  providerOptions: string[]
  onChange: (draft: DraftRow) => void
}) {
  const { t } = useTranslation()
  const editingGlobal = draft.original?.namespace === ''
  return (
    <>
      <TableCell>
        {editingGlobal ? (
          <Badge variant="secondary">
            {t('workflow.buildSettings.globalDefault')}
          </Badge>
        ) : (
          <div className="space-y-1">
            <Input
              value={draft.namespace}
              // Existing rows are identified by their namespace; changing it
              // means deleting the row and adding a new one.
              disabled={!draft.isNew}
              onChange={(e) =>
                onChange({ ...draft, namespace: e.target.value })
              }
              placeholder={t('workflow.buildSettings.namespacePlaceholder')}
              className="w-56 font-mono"
            />
            <p className="text-[11px] text-muted-foreground">
              {t('workflow.buildSettings.namespaceHint')}
            </p>
          </div>
        )}
      </TableCell>
      <TableCell>
        {providerOptions.length > 0 ? (
          <Select
            value={draft.provider}
            onValueChange={(provider) => onChange({ ...draft, provider })}
          >
            <SelectTrigger className="w-56">
              <SelectValue
                placeholder={t('workflow.buildSettings.providerPlaceholder')}
              />
            </SelectTrigger>
            <SelectContent>
              {providerOptions.map((provider) => (
                <SelectItem key={provider} value={provider}>
                  {provider}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : (
          <Input
            value={draft.provider}
            onChange={(e) => onChange({ ...draft, provider: e.target.value })}
            placeholder={t('workflow.buildSettings.providerInputPlaceholder')}
            className="w-56 font-mono"
          />
        )}
      </TableCell>
    </>
  )
}
