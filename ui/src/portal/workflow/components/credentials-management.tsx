// Credentials management (workflow-design §9, minimal M4 UI): two sections —
// Git credentials (git fetch auth) and registry credentials (image push /
// pull auth) — each listing shape summaries (name / description / hosts /
// non-secret accounts) with create + delete. Payloads are write-only —
// passwords are never echoed back; "edit" pre-fills the non-secret fields
// and replaces description + payload through the module's atomic PUT, with
// blank passwords keeping the stored secrets (server-side merge).
import '@/portal/workflow/i18n'

import { useEffect, useState } from 'react'
import {
  DockerIcon,
  GitIcon,
} from '@/portal/application/components/brand-icons'
import { ActionTable } from '@/portal/components/action-table'
import { IconPencil, IconPlus, IconTrash } from '@tabler/icons-react'
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
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { DeleteConfirmationDialog } from '@/components/delete-confirmation-dialog'

import {
  useCreateCredentialMutation,
  useCredentials,
  useDeleteCredentialMutation,
  useUpdateCredentialMutation,
  WorkflowApiError,
} from '../api'
import type { CredentialSummary, RegistryCredentialPayload } from '../types'

type CredentialKind = 'git' | 'registry'

interface CredentialForm {
  name: string
  description: string
  gitHost: string
  gitUsername: string
  gitPassword: string
  registry: RegistryCredentialPayload[]
}

const emptyForm: CredentialForm = {
  name: '',
  description: '',
  gitHost: '',
  gitUsername: '',
  gitPassword: '',
  registry: [{ host: '', username: '', password: '' }],
}

// keepBlankPasswords (edit mode) keeps rows whose password is empty — the
// server merges them with the stored credential; create mode requires every
// row to be complete up front.
function payloadOf(
  kind: CredentialKind,
  form: CredentialForm,
  keepBlankPasswords: boolean
) {
  if (kind === 'git') {
    if (
      form.gitUsername === '' ||
      (!keepBlankPasswords && form.gitPassword === '')
    ) {
      return null
    }
    return {
      git: {
        host: form.gitHost.trim() || undefined,
        username: form.gitUsername,
        password: form.gitPassword,
      },
    }
  }
  const registry = form.registry.filter(
    (r) =>
      r.host !== '' &&
      r.username !== '' &&
      (keepBlankPasswords || r.password !== '')
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

  // One shared dialog: the section that opened it fixes the payload kind, so
  // the form never needs the type selector.
  const [dialogKind, setDialogKind] = useState<CredentialKind>('git')
  // The active table tab doubles as the create button's payload kind.
  const [kind, setKind] = useState<CredentialKind>('git')
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

  const openCreate = (kind: CredentialKind) => {
    setDialogKind(kind)
    setForm(emptyForm)
    setEditingName(null)
    setDialogOpen(true)
  }

  const openReplace = (kind: CredentialKind, credential: CredentialSummary) => {
    setDialogKind(kind)
    setForm({
      ...emptyForm,
      name: credential.name,
      description: credential.description,
      // Non-secret account fields are echoed back; secrets never are — blank
      // passwords keep the stored ones through the server-side merge.
      gitHost: credential.gitHost,
      gitUsername: credential.gitUsername,
      registry:
        kind === 'registry' && credential.registryAccounts.length > 0
          ? credential.registryAccounts.map((account) => ({
              host: account.host,
              username: account.username,
              password: '',
            }))
          : emptyForm.registry,
    })
    setEditingName(credential.name)
    setDialogOpen(true)
  }

  const submit = () => {
    const payload = payloadOf(dialogKind, form, editingName !== null)
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
      // no window exists where the credential is missing. Blank passwords
      // ride along; the server merges them with the stored secrets.
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

  const gitCredentials = (credentials ?? []).filter((c) => c.hasGit)
  const registryCredentials = (credentials ?? []).filter(
    (c) => c.registryHosts.length > 0
  )
  // Both kinds share one table (identical columns) behind a tab switch, so
  // the column layout never diverges between the two credential kinds.
  const kindCredentials = kind === 'git' ? gitCredentials : registryCredentials

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader className="flex flex-row items-start justify-between space-y-0">
          <div className="space-y-1">
            <CardTitle>{t('workflow.credentials.title')}</CardTitle>
            <CardDescription>
              {kind === 'git'
                ? t('workflow.credentials.gitDescription')
                : t('workflow.credentials.registryDescription')}
            </CardDescription>
          </div>
          <Button
            size="icon"
            onClick={() => openCreate(kind)}
            title={t('workflow.credentials.create')}
          >
            <IconPlus className="h-4 w-4" />
          </Button>
        </CardHeader>
        <CardContent className="space-y-4">
          <Tabs
            value={kind}
            onValueChange={(v) => setKind(v as CredentialKind)}
          >
            <TabsList>
              <TabsTrigger value="git" className="gap-1.5">
                <GitIcon className="h-4 w-4" />
                {t('workflow.credentials.gitTitle')}
              </TabsTrigger>
              <TabsTrigger value="registry" className="gap-1.5">
                <DockerIcon className="h-4 w-4" />
                {t('workflow.credentials.registryTitle')}
              </TabsTrigger>
            </TabsList>
          </Tabs>
          <ActionTable<CredentialSummary>
            data={kindCredentials}
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
                cell: ({ row }) => {
                  if (kind === 'git') {
                    return row.original.gitHost ? (
                      <Badge variant="secondary" className="font-mono">
                        {row.original.gitHost}
                      </Badge>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )
                  }
                  if (row.original.registryHosts.length === 0) {
                    return <span className="text-muted-foreground">—</span>
                  }
                  return (
                    <div className="flex flex-col items-start gap-1.5">
                      {row.original.registryHosts.map((host) => (
                        <Badge
                          key={host}
                          variant="outline"
                          className="font-mono"
                        >
                          {host}
                        </Badge>
                      ))}
                    </div>
                  )
                },
              },
              {
                id: 'accounts',
                header: t('workflow.credentials.accounts'),
                cell: ({ row }) => {
                  if (kind === 'git') {
                    return (
                      <Badge variant="outline" className="font-mono">
                        {row.original.gitUsername}
                      </Badge>
                    )
                  }
                  if (row.original.registryAccounts.length === 0) {
                    return <span className="text-muted-foreground">—</span>
                  }
                  return (
                    <div className="flex flex-col items-start gap-1.5">
                      {row.original.registryAccounts.map((account) => (
                        <Badge
                          key={account.host}
                          variant="outline"
                          className="font-mono"
                        >
                          {account.username}
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
            actions={[]}
            actionCellPrefix={(credential) => (
              // Direct icon buttons like the build-provider table — two actions
              // don't warrant the dropdown menu.
              <>
                <Button
                  variant="ghost"
                  size="icon"
                  className="text-muted-foreground hover:text-primary"
                  onClick={() => openReplace(kind, credential)}
                  title={t('common.actions.edit')}
                >
                  <IconPencil className="h-4 w-4" />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  className="text-muted-foreground hover:text-destructive"
                  onClick={() => setDeleteTarget(credential)}
                  title={t('common.actions.delete')}
                >
                  <IconTrash className="h-4 w-4" />
                </Button>
              </>
            )}
          />
          {isLoading && (
            <p className="py-4 text-center text-sm text-muted-foreground">
              {t('common.messages.loading')}
            </p>
          )}
          {!isLoading && kindCredentials.length === 0 && (
            <p className="py-4 text-center text-sm text-muted-foreground">
              {t(
                kind === 'git'
                  ? 'workflow.credentials.gitEmpty'
                  : 'workflow.credentials.registryEmpty'
              )}
            </p>
          )}
        </CardContent>
      </Card>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>
              {editingName
                ? t('workflow.credentials.editTitle')
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

            {dialogKind === 'git' ? (
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
                      placeholder={
                        editingName
                          ? t('workflow.credentials.keepPasswordPlaceholder')
                          : undefined
                      }
                      autoComplete="new-password"
                    />
                  </div>
                </div>
              </>
            ) : (
              <div className="space-y-2">
                <div className="grid grid-cols-[1fr_1fr_1fr_auto] gap-2">
                  <Label className="text-xs">
                    {t('workflow.credentials.host')}
                  </Label>
                  <Label className="text-xs">
                    {t('workflow.credentials.username')}
                  </Label>
                  <Label className="text-xs">
                    {t('workflow.credentials.password')}
                  </Label>
                  {/* match the row trash button's width so the header's 1fr
                      tracks line up with the rows' */}
                  <span className="w-9" />
                </div>
                {form.registry.map((entry, index) => (
                  <div
                    key={index}
                    className="grid grid-cols-[1fr_1fr_1fr_auto] items-center gap-2"
                  >
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
                    <Input
                      value={entry.username}
                      onChange={(e) =>
                        setForm({
                          ...form,
                          registry: form.registry.map((r, i) =>
                            i === index ? { ...r, username: e.target.value } : r
                          ),
                        })
                      }
                      className="text-xs"
                      autoComplete="off"
                    />
                    <Input
                      type="password"
                      value={entry.password}
                      onChange={(e) =>
                        setForm({
                          ...form,
                          registry: form.registry.map((r, i) =>
                            i === index ? { ...r, password: e.target.value } : r
                          ),
                        })
                      }
                      placeholder={
                        editingName
                          ? t('workflow.credentials.keepPasswordPlaceholder')
                          : undefined
                      }
                      className="text-xs"
                      autoComplete="new-password"
                    />
                    <Button
                      variant="ghost"
                      size="icon"
                      disabled={form.registry.length === 1}
                      className="text-muted-foreground hover:text-destructive"
                      title={t('workflow.credentials.removeRegistry')}
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
                    variant="ghost"
                    size="icon"
                    className="text-muted-foreground hover:text-primary"
                    title={t('workflow.credentials.addRegistry')}
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
                    <IconPlus className="h-4 w-4" />
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
    </div>
  )
}
