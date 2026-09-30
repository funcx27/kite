import { useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  forceDeleteApplicationPod,
  useApplicationResources,
} from '@/lib/api/applications'
import { translateError } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { LogViewer } from '../log-viewer'
import { PodMonitoring } from '../pod-monitoring'
import { Terminal } from '../terminal-content'

interface PodMonitoringDialogProps {
  appName: string
  namespace: string
  cluster: string
  podName: string
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Tab shown when the dialog opens (row buttons jump straight in). */
  initialView?: 'monitoring' | 'terminal' | 'logs'
}

type View = 'monitoring' | 'terminal' | 'logs'

// Pod monitoring dialog (design §10/§11): CPU/memory curves with a time
// range switch, plus direct terminal / logs / force delete. Terminal and
// logs reuse the kite WebSocket endpoints addressed via x-cluster-name.
export function PodMonitoringDialog({
  appName,
  namespace,
  cluster,
  podName,
  open,
  onOpenChange,
  initialView = 'monitoring',
}: PodMonitoringDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [view, setView] = useState<View>(initialView)
  const [deleting, setDeleting] = useState(false)
  // The terminal picks its container from this list; without it the shell
  // never starts (the WS connect requires a selected container).
  const { data: runtime } = useApplicationResources(appName, cluster, {
    disable: !open,
  })
  const podContainers = useMemo(
    () =>
      runtime?.pods?.items.find((pod) => pod.name === podName)?.containers ??
      [],
    [runtime, podName]
  )

  if (!open) return null

  const handleForceDelete = async () => {
    setDeleting(true)
    try {
      await forceDeleteApplicationPod(appName, cluster, podName)
      toast.success(t('applications.detail.podForceDeleted', { pod: podName }))
      await queryClient.invalidateQueries({
        queryKey: ['applications', appName, 'resources', cluster],
      })
      onOpenChange(false)
    } catch (err) {
      toast.error(translateError(err, t))
    } finally {
      setDeleting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85vh] sm:max-w-4xl flex-col overflow-hidden">
        <DialogHeader>
          <DialogTitle className="flex flex-wrap items-center gap-2 font-mono text-sm">
            {podName}
            <span className="font-sans text-xs text-muted-foreground">
              · {cluster} · {namespace}
            </span>
          </DialogTitle>
          <DialogDescription className="sr-only">
            {t('applications.detail.podMonitoring')}
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-wrap items-center gap-2">
          <Tabs
            value={view}
            onValueChange={(value) => setView(value as View)}
            className="w-auto"
          >
            <TabsList>
              <TabsTrigger value="monitoring">
                {t('applications.detail.podMonitoring')}
              </TabsTrigger>
              <TabsTrigger value="terminal">
                {t('applications.detail.terminal')}
              </TabsTrigger>
              <TabsTrigger value="logs">
                {t('applications.detail.logs')}
              </TabsTrigger>
            </TabsList>
          </Tabs>
          <Button
            size="sm"
            variant="outline"
            className="ml-auto text-destructive hover:bg-destructive/10 hover:text-destructive"
            disabled={deleting}
            onClick={() => void handleForceDelete()}
            title={t('applications.detail.forceDeleteHint')}
          >
            {t('applications.detail.forceDeletePod')}
          </Button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto">
          {view === 'monitoring' && (
            <PodMonitoring
              namespace={namespace}
              podName={podName}
              cluster={cluster}
            />
          )}
          {view === 'terminal' && (
            <div className="h-[60vh]">
              <Terminal
                namespace={namespace}
                podName={podName}
                cluster={cluster}
                containers={podContainers.map((name) => ({ name }))}
              />
            </div>
          )}
          {view === 'logs' && (
            <div className="h-[60vh]">
              <LogViewer
                namespace={namespace}
                podName={podName}
                cluster={cluster}
                containers={podContainers.map((name) => ({ name }))}
              />
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}
