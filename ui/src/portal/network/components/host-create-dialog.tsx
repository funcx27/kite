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
import { Input } from '@/components/ui/input'
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

interface HostCreateDialogProps {
  hosts: NetworkHostOverview[]
  cluster: string
  onClose: () => void
  onCreate: (input: {
    host: string
    ingressClass: string
    tlsCertificate: NetworkCertificate | null
  }) => void
}

// Host-centric creation: only the host entity is created here, bound to the
// view's cluster; routes are added on the host card afterwards.
export function HostCreateDialog({
  hosts,
  cluster,
  onClose,
  onCreate,
}: HostCreateDialogProps) {
  const { t } = useTranslation()
  const [host, setHost] = useState('')
  const [ingressClass, setIngressClass] = useState(mockIngressClasses[0])
  const [cert, setCert] = useState(CERT_NONE)

  const hostName = host.trim()
  // The same name on another cluster is an independent registration; only
  // this cluster's names collide.
  const duplicate =
    hostName !== '' &&
    hosts.some((h) => h.host === hostName && h.cluster === cluster)
  const invalid = hostName === '' || duplicate

  const create = () => {
    if (invalid) return
    onCreate({
      host: hostName,
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
          <DialogTitle>{t('network.hostCreate.title')}</DialogTitle>
          <DialogDescription>
            {t('network.hostCreate.description')}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div className="space-y-1.5">
            <Label>{t('network.hostCreate.host')}</Label>
            <Input
              value={host}
              onChange={(event) => setHost(event.target.value)}
              placeholder={t('network.hostCreate.hostPlaceholder')}
              className="font-mono"
            />
            {duplicate && (
              <p className="text-xs text-destructive">
                {t('network.hostCreate.duplicate')}
              </p>
            )}
          </div>
          <div className="space-y-1.5">
            <Label>{t('network.hostCreate.cluster')}</Label>
            <p className="font-mono text-xs text-muted-foreground">{cluster}</p>
          </div>
          <div className="space-y-1.5">
            <Label>{t('network.hostCreate.ingressClass')}</Label>
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
            <Label>{t('network.hostCreate.certificate')}</Label>
            <Select value={cert} onValueChange={setCert}>
              <SelectTrigger className="w-full font-mono">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={CERT_NONE}>
                  {t('network.hostCreate.certNone')}
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
          {hostName === '' && (
            <p className="mr-auto text-xs text-destructive">
              {t('network.hostCreate.invalid')}
            </p>
          )}
          <Button variant="outline" onClick={onClose}>
            {t('network.hostCreate.cancel')}
          </Button>
          <Button disabled={invalid} onClick={create}>
            {t('network.hostCreate.create')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
