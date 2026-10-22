import { useState } from 'react'
import { ActionTable } from '@/portal/components/action-table'
import {
  NetworkApiError,
  useAddAnnotationKeyMutation,
  useAnnotationKeys,
  useDeleteAnnotationKeyMutation,
  useImportAnnotationKeysMutation,
  useUpdateAnnotationKeyMutation,
} from '@/portal/network/api'
import { type NetworkAnnotationKey } from '@/portal/network/types'
import { ColumnDef } from '@tanstack/react-table'
import { ArrowLeft, Plus, Upload } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'

import { usePageTitle } from '@/hooks/use-page-title'
import { Badge } from '@/components/ui/badge'
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
import { Textarea } from '@/components/ui/textarea'

interface KeyForm {
  key: string
  ingressClass: string
  description: string
  defaultValue: string
}

const EMPTY_FORM: KeyForm = {
  key: '',
  ingressClass: '',
  description: '',
  defaultValue: '',
}

// Settings sub-page (global — no space dimension): the platform annotation
// key catalog. The network page's annotation dialog picks keys from this
// list, filtered by the host's IngressClass. Writes go through the admin
// API; the IngressClass is free-form because the catalog is not tied to any
// single cluster's class list.
export function AnnotationKeysPage() {
  const { t } = useTranslation()
  const { data: keysData, isLoading } = useAnnotationKeys()
  const keys = keysData ?? []
  const addMutation = useAddAnnotationKeyMutation()
  const updateMutation = useUpdateAnnotationKeyMutation()
  const deleteMutation = useDeleteAnnotationKeyMutation()
  const importMutation = useImportAnnotationKeysMutation()
  usePageTitle(t('network.annotationKeys.title'))

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<NetworkAnnotationKey | null>(null)
  const [form, setForm] = useState<KeyForm>(EMPTY_FORM)
  const [importOpen, setImportOpen] = useState(false)
  const [importClass, setImportClass] = useState('')
  const [importText, setImportText] = useState('')

  const openCreate = () => {
    setEditing(null)
    setForm(EMPTY_FORM)
    setDialogOpen(true)
  }

  const openEdit = (entry: NetworkAnnotationKey) => {
    setEditing(entry)
    setForm({
      key: entry.key,
      ingressClass: entry.ingressClass,
      description: entry.description,
      defaultValue: entry.defaultValue,
    })
    setDialogOpen(true)
  }

  const keyName = form.key.trim()
  const className = form.ingressClass.trim()
  const duplicate =
    editing === null && keyName !== '' && keys.some((k) => k.key === keyName)
  const invalid =
    keyName === '' ||
    className === '' ||
    form.description.trim() === '' ||
    duplicate

  const submit = () => {
    if (invalid) return
    if (editing) {
      updateMutation.mutate(
        {
          id: editing.id,
          req: {
            description: form.description.trim(),
            ingressClass: className,
            defaultValue: form.defaultValue.trim(),
          },
        },
        {
          onSuccess: () => {
            toast.success(
              t('network.annotationKeys.toast.updated', { key: editing.key })
            )
            setDialogOpen(false)
          },
          onError: (err) => toast.error(err.message),
        }
      )
    } else {
      addMutation.mutate(
        {
          key: keyName,
          description: form.description.trim(),
          ingressClass: className,
          defaultValue: form.defaultValue.trim(),
        },
        {
          onSuccess: () => {
            toast.success(
              t('network.annotationKeys.toast.added', { key: keyName })
            )
            setDialogOpen(false)
          },
          onError: (err) => toast.error(err.message),
        }
      )
    }
  }

  const importClassName = importClass.trim()
  const submitImport = () => {
    if (importClassName === '' || importText.trim() === '') return
    importMutation.mutate(
      { ingressClass: importClassName, text: importText },
      {
        onSuccess: ({ imported, skipped }) => {
          setImportOpen(false)
          setImportText('')
          toast.success(
            t('network.annotationKeys.toast.imported', {
              count: imported,
              skipped,
            })
          )
        },
        onError: (err) => toast.error(err.message),
      }
    )
  }

  const remove = (entry: NetworkAnnotationKey) => {
    deleteMutation.mutate(entry.id, {
      onSuccess: () =>
        toast.success(
          t('network.annotationKeys.toast.deleted', { key: entry.key })
        ),
      onError: (err) => {
        // A 409 delete carries the reference count; appending it makes the
        // "still in use" reason actionable.
        if (
          err instanceof NetworkApiError &&
          typeof err.payload.references === 'number'
        ) {
          toast.error(
            `${err.message} (${t('network.annotationKeys.toast.referenced', {
              count: err.payload.references,
            })})`
          )
          return
        }
        toast.error(err.message)
      },
    })
  }

  const columns: ColumnDef<NetworkAnnotationKey>[] = [
    {
      accessorKey: 'key',
      header: t('network.annotationKeys.columns.key'),
      cell: ({ row }) => (
        <span className="font-mono text-xs">{row.original.key}</span>
      ),
    },
    {
      accessorKey: 'ingressClass',
      header: t('network.annotationKeys.columns.class'),
      cell: ({ row }) => (
        <Badge variant="outline">{row.original.ingressClass}</Badge>
      ),
    },
    {
      accessorKey: 'defaultValue',
      header: t('network.annotationKeys.columns.defaultValue'),
      cell: ({ row }) => (
        <span className="font-mono text-xs">
          {row.original.defaultValue || '-'}
        </span>
      ),
    },
    {
      accessorKey: 'description',
      header: t('network.annotationKeys.columns.description'),
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground">
          {row.original.description}
        </span>
      ),
    },
  ]

  return (
    <div className="flex flex-col gap-4">
      <div className="space-y-1.5">
        <Button variant="ghost" size="sm" asChild className="h-7 px-2">
          <Link to="/settings">
            <ArrowLeft className="h-3.5 w-3.5" />
            {t('network.annotationKeys.backToSettings')}
          </Link>
        </Button>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 className="text-2xl font-bold">
              {t('network.annotationKeys.title')}
            </h1>
            <p className="text-sm text-muted-foreground">
              {t('network.annotationKeys.description')}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Button variant="outline" onClick={() => setImportOpen(true)}>
              <Upload className="h-3.5 w-3.5" />
              {t('network.annotationKeys.import')}
            </Button>
            <Button onClick={openCreate}>
              <Plus className="h-3.5 w-3.5" />
              {t('network.annotationKeys.add')}
            </Button>
          </div>
        </div>
      </div>

      {isLoading ? (
        <div className="py-6 text-center text-sm text-muted-foreground">
          {t('network.annotationKeys.loading')}
        </div>
      ) : (
        <ActionTable<NetworkAnnotationKey>
          data={keys}
          columns={columns}
          actions={[
            { label: t('network.actions.edit'), onClick: openEdit },
            {
              label: t('network.actions.delete'),
              onClick: remove,
            },
          ]}
        />
      )}

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>
              {editing
                ? t('network.annotationKeys.dialog.editTitle')
                : t('network.annotationKeys.dialog.addTitle')}
            </DialogTitle>
            <DialogDescription>
              {t('network.annotationKeys.dialog.description')}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label>{t('network.annotationKeys.dialog.key')}</Label>
              <Input
                value={editing ? editing.key : form.key}
                onChange={(event) =>
                  setForm((prev) => ({ ...prev, key: event.target.value }))
                }
                placeholder={t('network.annotationKeys.dialog.keyPlaceholder')}
                disabled={editing !== null}
                className="font-mono"
              />
              {duplicate && (
                <p className="text-xs text-destructive">
                  {t('network.annotationKeys.dialog.duplicate')}
                </p>
              )}
            </div>
            <div className="space-y-1.5">
              <Label>{t('network.annotationKeys.dialog.class')}</Label>
              <Input
                value={form.ingressClass}
                onChange={(event) =>
                  setForm((prev) => ({
                    ...prev,
                    ingressClass: event.target.value,
                  }))
                }
                placeholder={t(
                  'network.annotationKeys.dialog.classPlaceholder'
                )}
                className="font-mono"
              />
            </div>
            <div className="space-y-1.5">
              <Label>
                {t('network.annotationKeys.dialog.descriptionLabel')}
              </Label>
              <Input
                value={form.description}
                onChange={(event) =>
                  setForm((prev) => ({
                    ...prev,
                    description: event.target.value,
                  }))
                }
                placeholder={t(
                  'network.annotationKeys.dialog.descriptionPlaceholder'
                )}
              />
            </div>
            <div className="space-y-1.5">
              <Label>
                {t('network.annotationKeys.dialog.defaultValueLabel')}
              </Label>
              <Input
                value={form.defaultValue}
                onChange={(event) =>
                  setForm((prev) => ({
                    ...prev,
                    defaultValue: event.target.value,
                  }))
                }
                placeholder={t(
                  'network.annotationKeys.dialog.defaultValuePlaceholder'
                )}
                className="font-mono"
              />
            </div>
          </div>
          <DialogFooter>
            {(keyName === '' ||
              className === '' ||
              form.description.trim() === '') &&
              !duplicate && (
                <p className="mr-auto text-xs text-destructive">
                  {t('network.annotationKeys.dialog.invalid')}
                </p>
              )}
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              {t('network.annotationKeys.dialog.cancel')}
            </Button>
            <Button
              disabled={
                invalid || addMutation.isPending || updateMutation.isPending
              }
              onClick={submit}
            >
              {editing
                ? t('network.annotationKeys.dialog.save')
                : t('network.annotationKeys.dialog.create')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={importOpen} onOpenChange={setImportOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>
              {t('network.annotationKeys.importDialog.title')}
            </DialogTitle>
            <DialogDescription>
              {t('network.annotationKeys.importDialog.description')}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label>{t('network.annotationKeys.importDialog.class')}</Label>
              <Input
                value={importClass}
                onChange={(event) => setImportClass(event.target.value)}
                placeholder={t(
                  'network.annotationKeys.importDialog.classPlaceholder'
                )}
                className="font-mono"
              />
            </div>
            <div className="space-y-1.5">
              <Label>{t('network.annotationKeys.importDialog.lines')}</Label>
              <Textarea
                value={importText}
                onChange={(event) => setImportText(event.target.value)}
                placeholder={t(
                  'network.annotationKeys.importDialog.linesPlaceholder'
                )}
                className="min-h-32 font-mono text-xs"
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setImportOpen(false)}>
              {t('network.annotationKeys.importDialog.cancel')}
            </Button>
            <Button
              disabled={
                importClassName === '' ||
                importText.trim() === '' ||
                importMutation.isPending
              }
              onClick={submitImport}
            >
              {t('network.annotationKeys.importDialog.import')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
