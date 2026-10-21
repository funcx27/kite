import { useState } from 'react'
import {
  type NetworkHostOverview,
  type NetworkRoute,
} from '@/portal/network/types'
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
import { Label } from '@/components/ui/label'

interface RouteFormInput {
  path: string
  appName: string
  port: number
}

interface RouteDialogProps {
  host: NetworkHostOverview
  // null = add a new route; otherwise edit this one.
  editing: NetworkRoute | null
  onClose: () => void
  onSave: (input: RouteFormInput) => void
}

// Card-level route form: the host is fixed context, so the form only
// carries path/app/port. Duplicate paths are checked against the host's
// other routes — the edited route itself is excluded.
export function RouteDialog({
  host,
  editing,
  onClose,
  onSave,
}: RouteDialogProps) {
  const { t } = useTranslation()
  const [path, setPath] = useState(editing?.path ?? '/')
  const [appName, setAppName] = useState(editing?.appName ?? '')
  const [port, setPort] = useState(editing ? String(editing.port) : '')

  const normalizedPath = path.trim() || '/'
  const portNumber = Number(port)
  const valid =
    appName.trim() !== '' &&
    port !== '' &&
    Number.isInteger(portNumber) &&
    portNumber >= 1 &&
    portNumber <= 65535
  const pathTaken = host.routes.some(
    (route) => route.path === normalizedPath && route.path !== editing?.path
  )

  const save = () => {
    if (!valid || pathTaken) return
    onSave({
      path: normalizedPath,
      appName: appName.trim(),
      port: portNumber,
    })
    onClose()
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
          <DialogTitle>
            {editing
              ? t('network.routeDialog.titleEdit')
              : t('network.routeDialog.titleAdd')}
          </DialogTitle>
          <DialogDescription>
            {t('network.routeDialog.description', { host: host.host })}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div className="space-y-1.5">
            <Label>{t('network.routeDialog.path')}</Label>
            <Input
              value={path}
              onChange={(event) => setPath(event.target.value)}
              placeholder={t('network.routeDialog.pathPlaceholder')}
              className="font-mono"
            />
            {pathTaken && (
              <p className="text-xs text-destructive">
                {t('network.routeDialog.duplicate')}
              </p>
            )}
          </div>
          <div className="space-y-1.5">
            <Label>{t('network.routeDialog.app')}</Label>
            <Input
              value={appName}
              onChange={(event) => setAppName(event.target.value)}
              placeholder={t('network.routeDialog.appPlaceholder')}
              className="font-mono"
            />
          </div>
          <div className="space-y-1.5">
            <Label>{t('network.routeDialog.port')}</Label>
            <Input
              type="number"
              value={port}
              onChange={(event) => setPort(event.target.value)}
              placeholder={t('network.routeDialog.portPlaceholder')}
            />
          </div>
        </div>
        <DialogFooter>
          {!valid && (
            <p className="mr-auto text-xs text-destructive">
              {t('network.routeDialog.invalid')}
            </p>
          )}
          <Button variant="outline" onClick={onClose}>
            {t('network.routeDialog.cancel')}
          </Button>
          <Button disabled={!valid || pathTaken} onClick={save}>
            {t('network.routeDialog.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
