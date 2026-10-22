import { useOfflineHostMutation } from '@/portal/network/api'
import { type NetworkHostOverview } from '@/portal/network/types'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

interface OfflineDialogProps {
  host: NetworkHostOverview
  onClose: () => void
}

// Take-offline confirm dialog: the two buttons are the choice itself (the
// dialog is the second confirmation). "cluster" keeps the saved row so the
// host can be republished; "all" also deletes the saved config.
export function OfflineDialog({ host, onClose }: OfflineDialogProps) {
  const { t } = useTranslation()
  const offlineMutation = useOfflineHostMutation()

  const run = (scope: 'cluster' | 'all') => {
    offlineMutation.mutate(
      {
        space: host.space,
        cluster: host.cluster,
        host: host.host,
        scope,
      },
      {
        onSuccess: () => {
          toast.success(t('network.toast.offlined', { host: host.host }))
          onClose()
        },
        onError: (err) => toast.error(err.message),
      }
    )
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t('network.offlineDialog.title')}</DialogTitle>
          <DialogDescription>
            {t('network.offlineDialog.description', { host: host.host })}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2">
          <Button
            variant="outline"
            disabled={offlineMutation.isPending}
            className="h-auto w-full flex-col items-start gap-0.5 px-3 py-2.5"
            onClick={() => run('cluster')}
          >
            <span>{t('network.offlineDialog.clusterTitle')}</span>
            <span className="text-xs font-normal text-muted-foreground">
              {t('network.offlineDialog.clusterHint')}
            </span>
          </Button>
          <Button
            variant="destructive"
            disabled={offlineMutation.isPending}
            className="h-auto w-full flex-col items-start gap-0.5 px-3 py-2.5"
            onClick={() => run('all')}
          >
            <span>{t('network.offlineDialog.allTitle')}</span>
            <span className="text-xs font-normal opacity-80">
              {t('network.offlineDialog.allHint')}
            </span>
          </Button>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t('network.offlineDialog.cancel')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
