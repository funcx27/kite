import { useState } from 'react'
import { useCertificates } from '@/portal/application/api'
import {
  useNetworkIngressClasses,
  useUpdateHostSettingsMutation,
} from '@/portal/network/api'
import { familyOf } from '@/portal/network/ingress-family'
import { type NetworkHostOverview } from '@/portal/network/types'
import { format } from 'date-fns'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { hostCovered } from '@/lib/cert-coverage'
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
}

// Host-level settings: a class change rewrites the Ingress spec (needs
// republish), a cert change only swaps secret content (needs cert resync).
// The cluster binding is fixed at creation and shown read-only.
export function HostSettingsDialog({ host, onClose }: HostSettingsDialogProps) {
  const { t } = useTranslation()
  const updateMutation = useUpdateHostSettingsMutation()
  const { data: ingressClasses } = useNetworkIngressClasses(host.cluster)
  const { data: certificates } = useCertificates()
  const [ingressClass, setIngressClass] = useState(host.ingressClass)
  const [cert, setCert] = useState(host.tlsCertificate?.name ?? CERT_NONE)

  const classOptions = ingressClasses ?? []
  // Echo trick: a stored class (or cert) that the cluster (or cert store) no
  // longer lists still renders instead of a blank select.
  const classEchoed =
    ingressClass !== '' &&
    !classOptions.some((entry) => entry.name === ingressClass)
  const certEchoed =
    cert !== CERT_NONE && !(certificates ?? []).some((c) => c.name === cert)
  // Advisory only, mirroring the application wizard: the pair still saves.
  // ALB matches account certificates automatically, so no coverage warning.
  const selectedCert = (certificates ?? []).find(
    (certificate) => certificate.name === cert
  )
  const albAutoMatch = familyOf(ingressClass, classOptions) === 'alb'
  const certUncovered =
    !albAutoMatch &&
    selectedCert !== undefined &&
    !hostCovered(host.host.toLowerCase(), selectedCert)

  const save = () => {
    if (ingressClass === '') return
    updateMutation.mutate(
      {
        space: host.space,
        cluster: host.cluster,
        host: host.host,
        req: {
          ingressClass,
          tlsCertificate: cert === CERT_NONE ? null : cert,
        },
      },
      {
        onSuccess: () => {
          toast.success(
            t('network.toast.hostSettingsSaved', { host: host.host })
          )
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
                {classOptions.map((entry) => (
                  <SelectItem
                    key={entry.name}
                    value={entry.name}
                    className="font-mono"
                  >
                    {entry.name}
                  </SelectItem>
                ))}
                {classEchoed && (
                  <SelectItem value={ingressClass} className="font-mono">
                    {ingressClass}
                  </SelectItem>
                )}
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
                {(certificates ?? []).map((certificate) => (
                  <SelectItem key={certificate.name} value={certificate.name}>
                    {certificate.name} ·{' '}
                    {format(new Date(certificate.notAfter), 'yyyy-MM-dd')}
                  </SelectItem>
                ))}
                {certEchoed && <SelectItem value={cert}>{cert}</SelectItem>}
              </SelectContent>
            </Select>
            {albAutoMatch && (
              <p className="text-xs text-muted-foreground">
                {t('network.hostSettings.certAutoMatch')}
              </p>
            )}
            {certUncovered && (
              <p className="text-xs text-amber-600 dark:text-amber-400">
                {t('network.hostSettings.certUncovered', { host: host.host })}
              </p>
            )}
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t('network.hostSettings.cancel')}
          </Button>
          <Button disabled={updateMutation.isPending} onClick={save}>
            {t('network.hostSettings.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
