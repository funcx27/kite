import { useEffect, useState } from 'react'
import {
  ApplicationApiError,
  offlineApplicationConfig,
} from '@/application/api'
import type {
  Application,
  ApplicationOfflineResponse,
} from '@/application/types'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { translateError } from '@/lib/utils'
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
  app: Application
  cluster: string
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Receives the offline result (configDeleted / appUndeployed). */
  onOfflined: (result: ApplicationOfflineResponse) => void
}

// Cluster-level offline (design §6/§11): one destructive action — the cluster
// objects and the config row with its revision chain are deleted together and
// cannot be restored; deploying again starts from the app's definition.
export function OfflineDialog({
  app,
  cluster,
  open,
  onOpenChange,
  onOfflined,
}: OfflineDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [offlining, setOfflining] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (open) setError('')
  }, [open])

  const handleOffline = async () => {
    setOfflining(true)
    setError('')
    try {
      // A 200 always means everything was deleted (the backend answers 409
      // and keeps the config when any cluster deletion failed), so the
      // success result needs no partial-failure branch here.
      const result = await offlineApplicationConfig(
        app.namespace,
        app.name,
        cluster
      )
      await queryClient.invalidateQueries({ queryKey: ['applications'] })
      toast.success(
        result.clusterGone
          ? t('applications.offline.clusterGone', { cluster })
          : t('applications.offline.purged', { cluster })
      )
      onOpenChange(false)
      onOfflined(result)
    } catch (err) {
      // The one 409 the offline endpoint returns is the partial cluster
      // deletion (config kept for retry); other errors pass through.
      setError(
        err instanceof ApplicationApiError && err.status === 409
          ? t('applications.offline.partialFailure')
          : translateError(err, t)
      )
    } finally {
      setOfflining(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            {t('applications.offline.title', { cluster, name: app.name })}
          </DialogTitle>
          <DialogDescription className="sr-only">
            {t('applications.offline.title', { cluster, name: app.name })}
          </DialogDescription>
        </DialogHeader>

        <p className="text-sm text-muted-foreground">
          {t('applications.offline.description')}
        </p>

        <div className="flex items-start gap-2 rounded-lg bg-muted/50 px-3 py-2.5 text-xs text-muted-foreground">
          <span>ℹ</span>
          <span>{t('applications.offline.deleteAppHint')}</span>
        </div>

        {error && <p className="text-xs text-destructive">{error}</p>}

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.actions.cancel')}
          </Button>
          <Button
            variant="destructive"
            disabled={offlining}
            onClick={() => void handleOffline()}
          >
            {t('applications.offline.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
