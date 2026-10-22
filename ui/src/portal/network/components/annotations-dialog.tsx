import { useMemo, useState } from 'react'
import {
  useAnnotationKeys,
  useNetworkIngressClasses,
} from '@/portal/network/api'
import {
  AnnotationRows,
  type AnnotationRow,
} from '@/portal/network/components/annotation-rows'
import { catalogEntryMatches } from '@/portal/network/ingress-family'
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

interface AnnotationsDialogProps {
  host: NetworkHostOverview
  onClose: () => void
  onSave: (host: string, annotations: Record<string, string>) => void
}

// Host-level annotation editor. Keys can only be picked from the global
// platform catalog (admin-maintained, hard whitelist by design), filtered
// by the host's IngressClass or its controller family; selecting a key
// prefills the catalog default value. Entries land on the host's single
// Ingress metadata and apply to every path.
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

  const { data: ingressClasses } = useNetworkIngressClasses(host.cluster)
  const { data: catalogData } = useAnnotationKeys()
  const catalog = useMemo(
    () =>
      (catalogData ?? []).filter((entry) =>
        catalogEntryMatches(
          entry.ingressClass,
          host.ingressClass,
          ingressClasses ?? []
        )
      ),
    [catalogData, host.ingressClass, ingressClasses]
  )

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
        <AnnotationRows rows={rows} onChange={setRows} catalog={catalog} />
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
