// Certificates management (global settings): TLS certificate-key pairs the
// application wizard's TLS picker references by name. The key is write-only
// (never echoed back); editing prefills metadata plus the PEM chain through
// the admin detail endpoint and submits only changed fields — a changed chain
// requires the matching key again, a blank key keeps the stored one.
import { useEffect, useState } from 'react'
import {
  ApplicationApiError,
  fetchAdminCertificate,
  useAdminCertificates,
  useCreateCertificateMutation,
  useDeleteCertificateMutation,
  useUpdateCertificateMutation,
} from '@/portal/application/api'
import type { CertificateMeta } from '@/portal/application/types'
import { ActionTable } from '@/portal/components/action-table'
import { IconPencil, IconPlus, IconTrash } from '@tabler/icons-react'
import { format } from 'date-fns'
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
import { Textarea } from '@/components/ui/textarea'
import { DeleteConfirmationDialog } from '@/components/delete-confirmation-dialog'

interface CertificateForm {
  name: string
  description: string
  certPem: string
  keyPem: string
}

const emptyForm: CertificateForm = {
  name: '',
  description: '',
  certPem: '',
  keyPem: '',
}

export function CertificatesManagement() {
  const { t } = useTranslation()

  const { data: certificates, isLoading } = useAdminCertificates()
  const createMutation = useCreateCertificateMutation()
  const updateMutation = useUpdateCertificateMutation()
  const deleteMutation = useDeleteCertificateMutation()

  const [dialogOpen, setDialogOpen] = useState(false)
  const [form, setForm] = useState<CertificateForm>(emptyForm)
  const [editing, setEditing] = useState<CertificateMeta | null>(null)
  // Chain prefetched for the edit form; a diff against it decides whether the
  // key becomes mandatory again (same rule the server enforces).
  const [loadedCertPem, setLoadedCertPem] = useState('')
  const [deleteTarget, setDeleteTarget] = useState<CertificateMeta | null>(null)

  // Closing the dialog drops whatever key material was typed but not
  // submitted (component memory only, cleared on exit).
  useEffect(() => {
    if (dialogOpen) return
    setForm(emptyForm)
    setEditing(null)
    setLoadedCertPem('')
  }, [dialogOpen])

  const openCreate = () => {
    setForm(emptyForm)
    setEditing(null)
    setDialogOpen(true)
  }

  const openEdit = async (certificate: CertificateMeta) => {
    setEditing(certificate)
    setForm(emptyForm)
    setLoadedCertPem('')
    setDialogOpen(true)
    try {
      const detail = await fetchAdminCertificate(certificate.id)
      setForm({
        name: detail.name,
        description: detail.description,
        certPem: detail.certPem,
        keyPem: '',
      })
      setLoadedCertPem(detail.certPem)
    } catch (err) {
      setDialogOpen(false)
      toast.error(err instanceof Error ? err.message : String(err))
    }
  }

  const certChanged = editing !== null && form.certPem !== loadedCertPem
  const keyMissing = certChanged && form.keyPem === ''

  const submit = () => {
    if (keyMissing) return
    const onDone = () => {
      toast.success(
        editing
          ? t('applications.certificates.updated')
          : t('applications.certificates.created')
      )
      setDialogOpen(false)
    }
    if (editing) {
      updateMutation.mutate(
        {
          id: editing.id,
          // Only changed fields ride along: an unchanged chain without a key
          // keeps both stored values.
          req: {
            description: form.description,
            ...(certChanged ? { certPem: form.certPem } : {}),
            ...(form.keyPem !== '' ? { keyPem: form.keyPem } : {}),
          },
        },
        { onSuccess: onDone, onError: (err) => toast.error(err.message) }
      )
      return
    }
    createMutation.mutate(
      {
        name: form.name.trim(),
        description: form.description,
        certPem: form.certPem,
        keyPem: form.keyPem,
      },
      { onSuccess: onDone, onError: (err) => toast.error(err.message) }
    )
  }

  const pending =
    createMutation.isPending ||
    updateMutation.isPending ||
    deleteMutation.isPending
  const dialogError: ApplicationApiError | null =
    createMutation.error instanceof ApplicationApiError
      ? createMutation.error
      : updateMutation.error instanceof ApplicationApiError
        ? updateMutation.error
        : null
  const conflict = dialogError !== null && dialogError.status === 409

  const submitDisabled =
    pending ||
    form.name.trim() === '' ||
    form.certPem.trim() === '' ||
    (editing === null && form.keyPem.trim() === '') ||
    keyMissing

  return (
    <div>
      <Card>
        <CardHeader className="flex flex-row items-start justify-between space-y-0">
          <div className="space-y-1">
            <CardTitle>{t('applications.certificates.title')}</CardTitle>
            <CardDescription>
              {t('applications.certificates.description')}
            </CardDescription>
          </div>
          <Button
            size="icon"
            onClick={openCreate}
            title={t('applications.certificates.create')}
          >
            <IconPlus className="h-4 w-4" />
          </Button>
        </CardHeader>
        <CardContent className="space-y-4">
          <ActionTable<CertificateMeta>
            data={certificates ?? []}
            columns={[
              {
                accessorKey: 'name',
                header: t('applications.certificates.name'),
                cell: ({ row }) => (
                  <span className="font-mono font-medium">
                    {row.original.name}
                  </span>
                ),
              },
              {
                id: 'domains',
                header: t('applications.certificates.domains'),
                cell: ({ row }) => {
                  const domains = [
                    row.original.commonName,
                    ...row.original.dnsNames.filter(
                      (name) => name !== row.original.commonName
                    ),
                  ].filter(Boolean)
                  if (domains.length === 0) {
                    return <span className="text-muted-foreground">—</span>
                  }
                  return (
                    <span
                      className="block max-w-56 truncate font-mono text-xs"
                      title={domains.join(', ')}
                    >
                      {domains.join(', ')}
                    </span>
                  )
                },
              },
              {
                id: 'validity',
                header: t('applications.certificates.validity'),
                cell: ({ row }) => {
                  const notAfter = new Date(row.original.notAfter)
                  const daysLeft = Math.floor(
                    (notAfter.getTime() - Date.now()) / 86_400_000
                  )
                  return (
                    <div className="flex flex-col items-start gap-1">
                      <span className="text-xs text-muted-foreground">
                        {format(new Date(row.original.notBefore), 'yyyy-MM-dd')}{' '}
                        ~ {format(notAfter, 'yyyy-MM-dd')}
                      </span>
                      {daysLeft < 0 ? (
                        <Badge variant="outline" className="text-destructive">
                          {t('applications.certificates.expired')}
                        </Badge>
                      ) : daysLeft <= 30 ? (
                        <Badge
                          variant="outline"
                          className="border-amber-300 text-amber-700 dark:border-amber-700 dark:text-amber-400"
                        >
                          {t('applications.certificates.daysLeft', {
                            count: daysLeft,
                          })}
                        </Badge>
                      ) : (
                        <Badge variant="outline">
                          {t('applications.certificates.daysLeft', {
                            count: daysLeft,
                          })}
                        </Badge>
                      )}
                    </div>
                  )
                },
              },
              {
                accessorKey: 'issuer',
                header: t('applications.certificates.issuer'),
                cell: ({ row }) => (
                  <span
                    className="block max-w-40 truncate text-xs text-muted-foreground"
                    title={row.original.issuer}
                  >
                    {row.original.issuer || '—'}
                  </span>
                ),
              },
              {
                accessorKey: 'fingerprint',
                header: t('applications.certificates.fingerprint'),
                cell: ({ row }) => (
                  <span
                    className="font-mono text-xs"
                    title={row.original.fingerprint}
                  >
                    {row.original.fingerprint.slice(0, 12)}
                  </span>
                ),
              },
              {
                accessorKey: 'createdAt',
                header: t('applications.certificates.createdAt'),
                cell: ({ row }) => (
                  <span className="text-xs text-muted-foreground">
                    {format(new Date(row.original.createdAt), 'yyyy-MM-dd')}
                  </span>
                ),
              },
            ]}
            actions={[]}
            actionCellPrefix={(certificate) => (
              // Direct icon buttons like the credentials table — two actions
              // don't warrant the dropdown menu.
              <>
                <Button
                  variant="ghost"
                  size="icon"
                  className="text-muted-foreground hover:text-primary"
                  onClick={() => void openEdit(certificate)}
                  title={t('common.actions.edit')}
                >
                  <IconPencil className="h-4 w-4" />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  className="text-muted-foreground hover:text-destructive"
                  onClick={() => setDeleteTarget(certificate)}
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
          {!isLoading && (certificates ?? []).length === 0 && (
            <p className="py-4 text-center text-sm text-muted-foreground">
              {t('applications.certificates.empty')}
            </p>
          )}
        </CardContent>
      </Card>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>
              {editing
                ? t('applications.certificates.editTitle')
                : t('applications.certificates.createTitle')}
            </DialogTitle>
            {editing && (
              <DialogDescription>
                {t('applications.certificates.editNote')}
              </DialogDescription>
            )}
          </DialogHeader>
          <div className="space-y-3">
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label className="text-xs">
                  {t('applications.certificates.name')}
                </Label>
                <Input
                  value={form.name}
                  disabled={editing !== null}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="example-com"
                  className="font-mono"
                />
                <p className="text-[11px] text-muted-foreground">
                  {t('applications.certificates.nameHint')}
                </p>
              </div>
              <div className="space-y-1.5">
                <Label className="text-xs">
                  {t('applications.certificates.descriptionLabel')}
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
              <Label className="text-xs">
                {t('applications.certificates.certPem')}
              </Label>
              <Textarea
                value={form.certPem}
                onChange={(e) => setForm({ ...form, certPem: e.target.value })}
                placeholder={t('applications.certificates.certPemPlaceholder')}
                className="min-h-28 font-mono text-xs"
                spellCheck={false}
              />
            </div>
            <div className="space-y-1.5">
              <Label className="text-xs">
                {t('applications.certificates.keyPem')}
              </Label>
              <Textarea
                value={form.keyPem}
                onChange={(e) => setForm({ ...form, keyPem: e.target.value })}
                placeholder={
                  editing
                    ? t('applications.certificates.keepKeyPlaceholder')
                    : t('applications.certificates.keyPemPlaceholder')
                }
                className="min-h-20 font-mono text-xs"
                spellCheck={false}
              />
              {keyMissing && (
                <p className="text-xs text-destructive">
                  {t('applications.certificates.keyRequired')}
                </p>
              )}
            </div>
            <p className="text-xs text-muted-foreground">
              {t('applications.certificates.createNote')}
            </p>
            {conflict && dialogError && (
              <p className="text-xs text-destructive">{dialogError.message}</p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              {t('common.actions.cancel')}
            </Button>
            <Button disabled={submitDisabled} onClick={submit}>
              {editing
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
          deleteMutation.mutate(deleteTarget.id, {
            onSuccess: () => {
              toast.success(t('applications.certificates.deleted'))
              setDeleteTarget(null)
            },
            onError: (err) => toast.error(err.message),
          })
        }}
        resourceName={deleteTarget?.name ?? ''}
        resourceType="certificate"
        additionalNote={t('applications.certificates.deleteDescription')}
      />
    </div>
  )
}
