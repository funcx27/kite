import { useState } from 'react'
import { ActionTable } from '@/portal/components/action-table'
import {
  addAnnotationKey,
  importAnnotationKeys,
  removeAnnotationKey,
  updateAnnotationKey,
  useAnnotationKeys,
} from '@/portal/network/annotation-key-store'
import { mockIngressClasses } from '@/portal/network/mock'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'

interface KeyForm {
  key: string
  ingressClass: string
  description: string
}

const EMPTY_FORM: KeyForm = {
  key: '',
  ingressClass: mockIngressClasses[0],
  description: '',
}

// Settings sub-page (global — no space dimension): the platform annotation
// key catalog. The network page's annotation dialog picks keys from this
// list, filtered by the host's IngressClass.
export function AnnotationKeysPage() {
  const { t } = useTranslation()
  const keys = useAnnotationKeys()
  usePageTitle(t('network.annotationKeys.title'))

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<NetworkAnnotationKey | null>(null)
  const [form, setForm] = useState<KeyForm>(EMPTY_FORM)
  const [importOpen, setImportOpen] = useState(false)
  const [importClass, setImportClass] = useState(mockIngressClasses[0])
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
    })
    setDialogOpen(true)
  }

  const keyName = form.key.trim()
  const duplicate =
    editing === null && keyName !== '' && keys.some((k) => k.key === keyName)
  const invalid = keyName === '' || form.description.trim() === '' || duplicate

  const submit = () => {
    if (invalid) return
    if (editing) {
      updateAnnotationKey(editing.key, {
        description: form.description.trim(),
        ingressClass: form.ingressClass,
      })
      toast.success(
        t('network.annotationKeys.toast.updated', { key: editing.key })
      )
    } else {
      addAnnotationKey({
        key: keyName,
        ingressClass: form.ingressClass,
        description: form.description.trim(),
      })
      toast.success(t('network.annotationKeys.toast.added', { key: keyName }))
    }
    setDialogOpen(false)
  }

  const submitImport = () => {
    const entries: NetworkAnnotationKey[] = []
    let malformed = 0
    for (const line of importText.split('\n')) {
      const trimmed = line.trim()
      if (trimmed === '') continue
      const sep = trimmed.search(/[,\t]/)
      const key = sep > 0 ? trimmed.slice(0, sep).trim() : ''
      const description = sep > 0 ? trimmed.slice(sep + 1).trim() : ''
      if (key === '' || description === '') {
        malformed++
        continue
      }
      entries.push({ key, description, ingressClass: importClass })
    }
    const { imported, skipped } = importAnnotationKeys(entries)
    setImportOpen(false)
    setImportText('')
    toast.success(
      t('network.annotationKeys.toast.imported', {
        count: imported,
        skipped: skipped + malformed,
      })
    )
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

      <ActionTable<NetworkAnnotationKey>
        data={keys}
        columns={columns}
        actions={[
          { label: t('network.actions.edit'), onClick: openEdit },
          {
            label: t('network.actions.delete'),
            onClick: (entry) => {
              removeAnnotationKey(entry.key)
              toast.success(
                t('network.annotationKeys.toast.deleted', { key: entry.key })
              )
            },
          },
        ]}
      />

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
              <Select
                value={form.ingressClass}
                onValueChange={(value) =>
                  setForm((prev) => ({ ...prev, ingressClass: value }))
                }
              >
                <SelectTrigger className="w-full font-mono">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {mockIngressClasses.map((name) => (
                    <SelectItem key={name} value={name} className="font-mono">
                      {name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
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
          </div>
          <DialogFooter>
            {(keyName === '' || form.description.trim() === '') &&
              !duplicate && (
                <p className="mr-auto text-xs text-destructive">
                  {t('network.annotationKeys.dialog.invalid')}
                </p>
              )}
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              {t('network.annotationKeys.dialog.cancel')}
            </Button>
            <Button disabled={invalid} onClick={submit}>
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
              <Select value={importClass} onValueChange={setImportClass}>
                <SelectTrigger className="w-full font-mono">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {mockIngressClasses.map((name) => (
                    <SelectItem key={name} value={name} className="font-mono">
                      {name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
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
            <Button disabled={importText.trim() === ''} onClick={submitImport}>
              {t('network.annotationKeys.importDialog.import')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
