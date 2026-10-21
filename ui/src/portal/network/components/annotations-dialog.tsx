import { useState } from 'react'
import { useAnnotationKeys } from '@/portal/network/annotation-key-store'
import { type NetworkHostOverview } from '@/portal/network/types'
import { useTranslation } from 'react-i18next'

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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

interface AnnotationRow {
  key: string
  value: string
}

interface AnnotationsDialogProps {
  host: NetworkHostOverview
  onClose: () => void
  onSave: (host: string, annotations: Record<string, string>) => void
}

// Host-level annotation editor. Keys can only be picked from the global
// platform catalog (admin-maintained, hard whitelist by design), filtered
// by the host's IngressClass; a key already chosen by another row is
// disabled, so duplicates are impossible by construction. The catalog
// description renders as help text under each row. Entries land on the
// host's single Ingress metadata and apply to every path.
export function AnnotationsDialog({
  host,
  onClose,
  onSave,
}: AnnotationsDialogProps) {
  const { t } = useTranslation()
  const [rows, setRows] = useState<AnnotationRow[]>(() =>
    Object.entries(host.annotations ?? {}).map(([key, value]) => ({
      key,
      value,
    }))
  )

  const catalog = useAnnotationKeys().filter(
    (entry) => entry.ingressClass === host.ingressClass
  )

  const update = (index: number, changes: Partial<AnnotationRow>) =>
    setRows((prev) =>
      prev.map((row, i) => (i === index ? { ...row, ...changes } : row))
    )

  const remove = (index: number) =>
    setRows((prev) => prev.filter((_, i) => i !== index))

  const add = () => setRows((prev) => [...prev, { key: '', value: '' }])

  const save = () => {
    const annotations: Record<string, string> = {}
    for (const row of rows) {
      if (row.key === '') continue
      annotations[row.key] = row.value
    }
    onSave(host.host, annotations)
    onClose()
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="flex flex-col sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('network.annotationsDialog.title')}</DialogTitle>
          <DialogDescription>
            {t('network.annotationsDialog.description', { host: host.host })}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2">
          {rows.length === 0 && (
            <p className="text-xs text-muted-foreground">
              {t('network.annotationsDialog.empty')}
            </p>
          )}
          {rows.map((row, index) => {
            const help = catalog.find(
              (entry) => entry.key === row.key
            )?.description
            return (
              <div key={index} className="space-y-1">
                <div className="flex items-center gap-2">
                  <Select
                    value={row.key}
                    onValueChange={(value) => update(index, { key: value })}
                  >
                    <SelectTrigger className="h-8 min-w-0 flex-1 truncate font-mono text-xs">
                      <SelectValue
                        className="truncate"
                        placeholder={t(
                          'network.annotationsDialog.keyPlaceholder'
                        )}
                      />
                    </SelectTrigger>
                    <SelectContent>
                      {catalog.map((entry) => (
                        <SelectItem
                          key={entry.key}
                          value={entry.key}
                          disabled={rows.some(
                            (other) => other.key === entry.key
                          )}
                          className="font-mono"
                        >
                          {entry.key}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <Input
                    value={row.value}
                    onChange={(event) =>
                      update(index, { value: event.target.value })
                    }
                    placeholder={t(
                      'network.annotationsDialog.valuePlaceholder'
                    )}
                    className="h-8 w-36 font-mono text-xs"
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="shrink-0 px-2"
                    title={t('network.annotationsDialog.remove')}
                    onClick={() => remove(index)}
                  >
                    ✕
                  </Button>
                </div>
                {help && (
                  <p className="pl-1 text-xs text-muted-foreground">{help}</p>
                )}
              </div>
            )
          })}
          <div className="flex items-center">
            <button
              type="button"
              className="cursor-pointer text-xs text-muted-foreground transition-colors hover:text-foreground"
              onClick={add}
            >
              ＋ {t('network.annotationsDialog.add')}
            </button>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t('network.annotationsDialog.cancel')}
          </Button>
          <Button onClick={save}>{t('network.annotationsDialog.save')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
