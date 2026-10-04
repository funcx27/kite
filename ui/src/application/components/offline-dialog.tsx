import { useEffect, useState } from 'react'
import { offlineApplicationConfig } from '@/application/api'
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

// Cluster-level offline (design §6/§11): radio cards — keep (default) keeps
// the config and its revisions; purge also deletes them irreversibly and
// turns the confirm button destructive.
export function OfflineDialog({
  app,
  cluster,
  open,
  onOpenChange,
  onOfflined,
}: OfflineDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [deleteConfig, setDeleteConfig] = useState(false)
  const [offlining, setOfflining] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (open) {
      setDeleteConfig(false)
      setError('')
    }
  }, [open])

  const handleOffline = async () => {
    setOfflining(true)
    setError('')
    try {
      const result = await offlineApplicationConfig(app.name, cluster, {
        deleteConfig,
      })
      await queryClient.invalidateQueries({ queryKey: ['applications'] })
      if (result.success) {
        toast.success(
          result.clusterGone
            ? t('applications.offline.clusterGone', { cluster })
            : deleteConfig
              ? t('applications.offline.purged', { cluster })
              : t('applications.offline.kept', { cluster })
        )
        onOpenChange(false)
        onOfflined(result)
      } else {
        toast.warning(t('applications.offline.partialFailure'))
      }
    } catch (err) {
      setError(translateError(err, t))
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

        <div className="space-y-2">
          <label
            className={
              !deleteConfig
                ? 'flex cursor-pointer items-start gap-3 rounded-lg border border-primary bg-muted/50 px-3 py-3'
                : 'flex cursor-pointer items-start gap-3 rounded-lg border px-3 py-3'
            }
          >
            <input
              type="radio"
              name="offline-mode"
              checked={!deleteConfig}
              onChange={() => setDeleteConfig(false)}
              className="mt-0.5 accent-primary"
            />
            <span>
              <span className="text-sm font-medium">
                {t('applications.offline.keepTitle')}
                <span className="ml-1.5 text-xs font-normal text-muted-foreground">
                  {t('common.fields.default', 'Default')}
                </span>
              </span>
              <span className="mt-0.5 block text-xs text-muted-foreground">
                {t('applications.offline.keepDescription')}
              </span>
            </span>
          </label>
          <label
            className={
              deleteConfig
                ? 'flex cursor-pointer items-start gap-3 rounded-lg border border-destructive/50 bg-destructive/5 px-3 py-3'
                : 'flex cursor-pointer items-start gap-3 rounded-lg border px-3 py-3'
            }
          >
            <input
              type="radio"
              name="offline-mode"
              checked={deleteConfig}
              onChange={() => setDeleteConfig(true)}
              className="mt-0.5 accent-destructive"
            />
            <span>
              <span className="text-sm font-medium text-destructive">
                {t('applications.offline.purgeTitle')}
              </span>
              <span className="mt-0.5 block text-xs text-muted-foreground">
                {t('applications.offline.purgeDescription')}
              </span>
            </span>
          </label>
        </div>

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
            variant={deleteConfig ? 'destructive' : 'default'}
            disabled={offlining}
            onClick={() => void handleOffline()}
          >
            {deleteConfig
              ? t('applications.offline.confirmPurge')
              : t('applications.offline.confirmKeep')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
