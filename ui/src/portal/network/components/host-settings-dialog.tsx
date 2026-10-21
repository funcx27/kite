import { useState } from 'react'
import { mockCertCatalog, mockIngressClasses } from '@/portal/network/mock'
import {
  type NetworkCertificate,
  type NetworkHostOverview,
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
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

// Radix Select items cannot carry an empty value; sentinel marks "no cert".
const CERT_NONE = '__none__'

interface HostSettingsDialogProps {
  host: NetworkHostOverview
  onClose: () => void
  onSave: (
    host: string,
    changes: {
      ingressClass: string
      tlsCertificate: NetworkCertificate | null
    }
  ) => void
}

// Host-level settings: a class change rewrites the Ingress spec (needs
// republish), a cert change only swaps secret content (needs cert resync).
// The cluster binding is fixed at creation and shown read-only.
export function HostSettingsDialog({
  host,
  onClose,
  onSave,
}: HostSettingsDialogProps) {
  const { t } = useTranslation()
  const [ingressClass, setIngressClass] = useState(host.ingressClass)
  const [cert, setCert] = useState(host.tlsCertificate?.name ?? CERT_NONE)

  const save = () => {
    onSave(host.host, {
      ingressClass,
      tlsCertificate:
        cert === CERT_NONE
          ? null
          : { name: cert, expiresAt: mockCertCatalog[cert] ?? '' },
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
          <DialogTitle>{t('network.hostSettings.title')}</DialogTitle>
          <DialogDescription>
            {t('network.hostSettings.description', { host: host.host })}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div className="space-y-1.5">
            <Label>{t('network.hostSettings.cluster')}</Label>
            <p className="font-mono text-xs text-muted-foreground">
              {host.cluster}
            </p>
          </div>
          <div className="space-y-1.5">
            <Label>{t('network.hostSettings.ingressClass')}</Label>
            <Select value={ingressClass} onValueChange={setIngressClass}>
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
            <Label>{t('network.hostSettings.certificate')}</Label>
            <Select value={cert} onValueChange={setCert}>
              <SelectTrigger className="w-full font-mono">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={CERT_NONE}>
                  {t('network.hostSettings.certNone')}
                </SelectItem>
                {Object.keys(mockCertCatalog).map((name) => (
                  <SelectItem key={name} value={name} className="font-mono">
                    {name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t('network.hostSettings.cancel')}
          </Button>
          <Button onClick={save}>{t('network.hostSettings.save')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
