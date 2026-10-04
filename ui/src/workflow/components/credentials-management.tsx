// Credentials management (workflow-design §9, minimal M4 UI): a settings
// section listing stored credentials as shape summaries (name / description /
// git flag / registry hosts / non-secret accounts) with create + delete.
// Payloads are write-only —
// nothing is ever echoed back; "edit" replaces description + payload through
// the module's atomic PUT, so all secret material must be re-entered.
import '@/workflow/i18n'

import { useEffect, useState } from 'react'
import { IconPlus, IconTrash } from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { formatDate } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { ActionTable } from '@/components/action-table'
import { DeleteConfirmationDialog } from '@/components/delete-confirmation-dialog'

import {
  useCreateCredentialMutation,
  useCredentials,
  useDeleteCredentialMutation,
  useUpdateCredentialMutation,
  WorkflowApiError,
} from '../api'
import type {
  CredentialPayload,
  CredentialSummary,
  RegistryCredentialPayload,
} from '../types'

type CredentialKind = 'git' | 'registry'

interface CredentialForm {
  name: string
  description: string
  kind: CredentialKind
  gitHost: string
  gitUsername: string
  gitPassword: string
  registry: RegistryCredentialPayload[]
}

const emptyForm: CredentialForm = {
  name: '',
  description: '',
  kind: 'git',
  gitHost: '',
  gitUsername: '',
  gitPassword: '',
  registry: [{ host: '', username: '', password: '' }],
}

function payloadOf(form: CredentialForm): CredentialPayload | null {
  if (form.kind === 'git') {
    if (form.gitUsername === '' || form.gitPassword === '') return null
    return {
      git: {
        host: form.gitHost.trim() || undefined,
        username: form.gitUsername,
        password: form.gitPassword,
      },
    }
  }
  const registry = form.registry.filter(
    (r) => r.host !== '' && r.username !== '' && r.password !== ''
  )
  if (registry.length === 0) return null
  return { registry }
}

export function CredentialsManagement() {
  const { t } = useTranslation()

  const { data: credentials, isLoading } = useCredentials()
  const createMutation = useCreateCredentialMutation()
  const updateMutation = useUpdateCredentialMutation()
  const deleteMutation = useDeleteCredentialMutation()

  const [dialogOpen, setDialogOpen] = useState(false)
  const [form, setForm] = useState<CredentialForm>(emptyForm)
  const [editingName, setEditingName] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<CredentialSummary | null>(
    null
  )

  // Closing the dialog drops whatever secret material was typed but not
  // submitted (component memory only, cleared on exit).
  useEffect(() => {
    if (dialogOpen) return
    setForm(emptyForm)
    setEditingName(null)
  }, [dialogOpen])

  const openCreate = () => {
    setForm(emptyForm)
    setEditingName(null)
    setDialogOpen(true)
  }

  const openReplace = (credential: CredentialSummary) => {
    setForm({
      ...emptyForm,
      name: credential.name,
      description: credential.description,
      // Non-secret shape summary; secrets themselves are never echoed back.
      gitHost: credential.gitHost,
    })
    setEditingName(credential.name)
    setDialogOpen(true)
  }

  const submit = () => {
    const payload = payloadOf(form)
    if (!payload) {
      toast.error(t('workflow.credentials.payloadRequired'))
      return
    }
    const onDone = () => {
      toast.success(
        editingName
          ? t('workflow.credentials.updated')
          : t('workflow.credentials.created')
      )
      setDialogOpen(false)
    }
    if (editingName) {
      // Atomic replace via PUT: description + payload swap in one request, so
      // no window exists where the credential is missing. Secrets are not
      // readable back, hence the full re-entry.
      updateMutation.mutate(
        { name: editingName, req: { description: form.description, payload } },
        { onSuccess: onDone, onError: (err) => toast.error(err.message) }
      )
      return
    }
    createMutation.mutate(
      { name: form.name.trim(), description: form.description, payload },
      { onSuccess: onDone, onError: (err) => toast.error(err.message) }
    )
  }

  const pending =
    createMutation.isPending ||
    updateMutation.isPending ||
    deleteMutation.isPending
  const dialogError: WorkflowApiError | null =
    createMutation.error instanceof WorkflowApiError
      ? createMutation.error
      : updateMutation.error instanceof WorkflowApiError
        ? updateMutation.error
        : null
  const conflict = dialogError !== null && dialogError.status === 409

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between space-y-0">
        <div className="space-y-1">
          <CardTitle className="flex items-center gap-2">
            {t('workflow.credentials.title')}
            <Badge variant="secondary">{t('workflow.globalBadge')}</Badge>
          </CardTitle>
          <CardDescription>
            {t('workflow.credentials.description')}
          </CardDescription>
        </div>
        <Button onClick={openCreate}>
          <IconPlus className="mr-1.5 h-4 w-4" />
          {t('workflow.credentials.create')}
        </Button>
      </CardHeader>
      <CardContent>
        <ActionTable<CredentialSummary>
          data={credentials ?? []}
          columns={[
            {
              accessorKey: 'name',
              header: t('workflow.credentials.name'),
              cell: ({ row }) => (
                <span className="font-mono font-medium">
                  {row.original.name}
                </span>
              ),
            },
            {
              accessorKey: 'description',
              header: t('workflow.credentials.descriptionLabel'),
              cell: ({ row }) => (
                <span className="text-muted-foreground">
                  {row.original.description || '—'}
                </span>
              ),
            },
            {
              id: 'shape',
              header: t('workflow.credentials.shape'),
              cell: ({ row }) => (
                <div className="flex flex-wrap gap-1.5">
                  {row.original.hasGit && (
                    <Badge variant="secondary">
                      {t('workflow.credentials.gitSummary')}
                    </Badge>
                  )}
                  {row.original.registryHosts.map((host) => (
                    <Badge key={host} variant="outline" className="font-mono">
                      {host}
                    </Badge>
                  ))}
                  {!row.original.hasGit &&
                    row.original.registryHosts.length === 0 &&
                    '—'}
                </div>
              ),
            },
            {
              id: 'accounts',
              header: t('workflow.credentials.accounts'),
              cell: ({ row }) => {
                const accounts = [
                  ...(row.original.hasGit
                    ? [
                        row.original.gitHost
                          ? `${row.original.gitHost}:${row.original.gitUsername}`
                          : row.original.gitUsername,
                      ]
                    : []),
                  ...row.original.registryAccounts.map(
                    (a) => `${a.host}:${a.username}`
                  ),
                ]
                if (accounts.length === 0) {
                  return <span className="text-muted-foreground">—</span>
                }
                return (
                  <div className="flex flex-wrap gap-1.5">
                    {accounts.map((account, i) => (
                      <Badge key={i} variant="outline" className="font-mono">
                        {account}
                      </Badge>
                    ))}
                  </div>
                )
              },
            },
            {
              accessorKey: 'updatedAt',
              header: t('workflow.list.updatedAt'),
              cell: ({ row }) => (
                <span className="text-xs text-muted-foreground">
                  {formatDate(row.original.updatedAt)}
                </span>
              ),
            },
          ]}
          actions={[
            {
              label: t('common.actions.edit'),
              onClick: openReplace,
            },
            {
              label: t('common.actions.delete'),
              onClick: (credential) => setDeleteTarget(credential),
            },
          ]}
        />
        {isLoading && (
          <p className="py-4 text-center text-sm text-muted-foreground">
            {t('common.messages.loading')}
          </p>
        )}
        {!isLoading && (credentials ?? []).length === 0 && (
          <p className="py-4 text-center text-sm text-muted-foreground">
            {t('workflow.credentials.empty')}
          </p>
        )}
      </CardContent>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>
              {editingName
                ? t('workflow.credentials.editTitle', { name: editingName })
                : t('workflow.credentials.createTitle')}
            </DialogTitle>
            {editingName && (
              <DialogDescription>
                {t('workflow.credentials.editNote')}
              </DialogDescription>
            )}
          </DialogHeader>
          <div className="space-y-3">
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label className="text-xs">
                  {t('workflow.credentials.name')}
                </Label>
                <Input
                  value={form.name}
                  disabled={editingName !== null}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="registry-mirror"
                  className="font-mono"
                />
                <p className="text-[11px] text-muted-foreground">
                  {t('workflow.credentials.nameHint')}
                </p>
              </div>
              <div className="space-y-1.5">
                <Label className="text-xs">
                  {t('workflow.credentials.descriptionLabel')}
                </Label>
                <Input
                  value={form.description}
                  onChange={(e) =>
                    setForm({ ...form, description: e.target.value })
                  }
                />
              </div>
            </div>

            <div className="space-y-1.5">
              <Label className="text-xs">{t('workflow.params.type')}</Label>
              <Select
                value={form.kind}
                onValueChange={(kind) =>
                  setForm({ ...form, kind: kind as CredentialKind })
                }
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="git">
                    {t('workflow.credentials.typeGit')}
                  </SelectItem>
                  <SelectItem value="registry">
                    {t('workflow.credentials.typeRegistry')}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>

            {form.kind === 'git' ? (
              <>
                <div className="space-y-1.5">
                  <Label className="text-xs">
                    {t('workflow.credentials.gitAddress')}
                  </Label>
                  <Input
                    value={form.gitHost}
                    onChange={(e) =>
                      setForm({ ...form, gitHost: e.target.value })
                    }
                    placeholder="git.example.com"
                    className="font-mono"
                    autoComplete="off"
                  />
                  <p className="text-[11px] text-muted-foreground">
                    {t('workflow.credentials.gitAddressHint')}
                  </p>
                </div>
                <div className="grid grid-cols-2 gap-3">
                  <div className="space-y-1.5">
                    <Label className="text-xs">
                      {t('workflow.credentials.username')}
                    </Label>
                    <Input
                      value={form.gitUsername}
                      onChange={(e) =>
                        setForm({ ...form, gitUsername: e.target.value })
                      }
                      autoComplete="off"
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label className="text-xs">
                      {t('workflow.credentials.password')}
                    </Label>
                    <Input
                      type="password"
                      value={form.gitPassword}
                      onChange={(e) =>
                        setForm({ ...form, gitPassword: e.target.value })
                      }
                      autoComplete="new-password"
                    />
                  </div>
                </div>
              </>
            ) : (
              <div className="space-y-2">
                {form.registry.map((entry, index) => (
                  <div
                    key={index}
                    className="grid grid-cols-[1fr_1fr_1fr_auto] items-end gap-2"
                  >
                    <div className="space-y-1">
                      <Label className="text-xs">
                        {t('workflow.credentials.host')}
                      </Label>
                      <Input
                        value={entry.host}
                        onChange={(e) =>
                          setForm({
                            ...form,
                            registry: form.registry.map((r, i) =>
                              i === index ? { ...r, host: e.target.value } : r
                            ),
                          })
                        }
                        placeholder="ghcr.io"
                        className="font-mono text-xs"
                      />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs">
                        {t('workflow.credentials.username')}
                      </Label>
                      <Input
                        value={entry.username}
                        onChange={(e) =>
                          setForm({
                            ...form,
                            registry: form.registry.map((r, i) =>
                              i === index
                                ? { ...r, username: e.target.value }
                                : r
                            ),
                          })
                        }
                        className="text-xs"
                        autoComplete="off"
                      />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs">
                        {t('workflow.credentials.password')}
                      </Label>
                      <Input
                        type="password"
                        value={entry.password}
                        onChange={(e) =>
                          setForm({
                            ...form,
                            registry: form.registry.map((r, i) =>
                              i === index
                                ? { ...r, password: e.target.value }
                                : r
                            ),
                          })
                        }
                        className="text-xs"
                        autoComplete="new-password"
                      />
                    </div>
                    <Button
                      variant="ghost"
                      size="icon"
                      disabled={form.registry.length === 1}
                      className="text-muted-foreground hover:text-destructive"
                      onClick={() =>
                        setForm({
                          ...form,
                          registry: form.registry.filter((_, i) => i !== index),
                        })
                      }
                    >
                      <IconTrash className="h-4 w-4" />
                    </Button>
                  </div>
                ))}
                <div className="flex items-center justify-between">
                  <p className="text-[11px] text-muted-foreground">
                    {t('workflow.credentials.hostHint')}
                  </p>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() =>
                      setForm({
                        ...form,
                        registry: [
                          ...form.registry,
                          { host: '', username: '', password: '' },
                        ],
                      })
                    }
                  >
                    <IconPlus className="mr-1 h-3.5 w-3.5" />
                    {t('workflow.credentials.addRegistry')}
                  </Button>
                </div>
              </div>
            )}

            <p className="text-xs text-muted-foreground">
              {t('workflow.credentials.createNote')}
            </p>
            {conflict && dialogError && (
              <p className="text-xs text-destructive">{dialogError.message}</p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              {t('common.actions.cancel')}
            </Button>
            <Button
              disabled={pending || form.name.trim() === ''}
              onClick={submit}
            >
              {editingName
                ? t('common.actions.confirm')
                : t('common.actions.create')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <DeleteConfirmationDialog
        open={deleteTarget !== null}
        onOpenChange={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (!deleteTarget) return
          deleteMutation.mutate(deleteTarget.name, {
            onSuccess: () => {
              toast.success(t('workflow.credentials.deleted'))
              setDeleteTarget(null)
            },
            onError: (err) => toast.error(err.message),
          })
        }}
        resourceName={deleteTarget?.name ?? ''}
        resourceType="credential"
        additionalNote={t('workflow.credentials.deleteDescription')}
      />
    </Card>
  )
}
