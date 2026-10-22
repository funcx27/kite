import { useState } from 'react'
import { useCertificates } from '@/portal/application/api'
import {
  useCreateHostMutation,
  useNetworkIngressClasses,
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

// Mirrors the backend's validateDNSHost: lowercase dot-separated DNS name,
// each label a DNS-1123 label, at most 253 characters.
const HOST_PATTERN =
  /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/

interface HostCreateDialogProps {
  space: string
  cluster: string
  hosts: NetworkHostOverview[]
  onClose: () => void
}

// Host-centric creation: only the host entity is created here, bound to the
// view's cluster; routes are added on the host card afterwards.
export function HostCreateDialog({
  space,
  cluster,
  hosts,
  onClose,
}: HostCreateDialogProps) {
  const { t } = useTranslation()
  const createMutation = useCreateHostMutation()
  // The class list must come from the target cluster: publishing validates
  // the class exists there, so only real options are offered.
  const { data: ingressClasses } = useNetworkIngressClasses(cluster)
  const { data: certificates } = useCertificates()
  const [host, setHost] = useState('')
  const [ingressClass, setIngressClass] = useState('')
  const [cert, setCert] = useState(CERT_NONE)

  // The backend lowercases on create, so validation and the duplicate check
  // both run on the normalized name.
  const hostName = host.trim().toLowerCase()
  const classOptions = ingressClasses ?? []
  // The query loads asynchronously; derive the effective selection so a
  // freshly loaded list defaults to its first entry.
  const selectedClass = classOptions.some(
    (entry) => entry.name === ingressClass
  )
    ? ingressClass
    : classOptions[0]?.name
  // The same name on another cluster is an independent registration; only
  // this cluster's names collide (the server remains authoritative via 409).
  const duplicate =
    hostName !== '' &&
    hosts.some((h) => h.host === hostName && h.cluster === cluster)
  const hostInvalid =
    hostName === '' || hostName.length > 253 || !HOST_PATTERN.test(hostName)
  const invalid = hostInvalid || duplicate || !selectedClass

  // Advisory only, mirroring the application wizard: the pair still saves.
  // ALB matches account certificates automatically, so no coverage warning.
  const selectedCert = (certificates ?? []).find(
    (certificate) => certificate.name === cert
  )
  const albAutoMatch = familyOf(selectedClass ?? '', classOptions) === 'alb'
  const certUncovered =
    !albAutoMatch &&
    selectedCert !== undefined &&
    hostName !== '' &&
    !hostInvalid &&
    !hostCovered(hostName, selectedCert)

  const create = () => {
    if (invalid || !selectedClass) return
    createMutation.mutate(
      {
        space,
        req: {
          host: hostName,
          cluster,
          ingressClass: selectedClass,
          tlsCertificate: cert === CERT_NONE ? undefined : cert,
        },
      },
      {
        onSuccess: () => {
          toast.success(t('network.toast.hostCreated', { host: hostName }))
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
            {!duplicate && hostName !== '' && hostInvalid && (
              <p className="text-xs text-destructive">
                {t('network.hostCreate.hostInvalid')}
              </p>
            )}
          </div>
          <div className="space-y-1.5">
            <Label>{t('network.hostCreate.cluster')}</Label>
            <p className="font-mono text-xs text-muted-foreground">{cluster}</p>
          </div>
          <div className="space-y-1.5">
            <Label>{t('network.hostCreate.ingressClass')}</Label>
            <Select value={selectedClass ?? ''} onValueChange={setIngressClass}>
              <SelectTrigger className="w-full font-mono">
                <SelectValue
                  placeholder={t('network.hostCreate.classPlaceholder')}
                />
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
                {(certificates ?? []).map((certificate) => (
                  <SelectItem key={certificate.name} value={certificate.name}>
                    {certificate.name} ·{' '}
                    {format(new Date(certificate.notAfter), 'yyyy-MM-dd')}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {albAutoMatch && (
              <p className="text-xs text-muted-foreground">
                {t('network.hostCreate.certAutoMatch')}
              </p>
            )}
            {certUncovered && (
              <p className="text-xs text-amber-600 dark:text-amber-400">
                {t('network.hostCreate.certUncovered', { host: hostName })}
              </p>
            )}
          </div>
        </div>
        <DialogFooter>
          {hostInvalid && (
            <p className="mr-auto text-xs text-destructive">
              {t('network.hostCreate.invalid')}
            </p>
          )}
          <Button variant="outline" onClick={onClose}>
            {t('network.hostCreate.cancel')}
          </Button>
          <Button
            disabled={invalid || createMutation.isPending}
            onClick={create}
          >
            {t('network.hostCreate.create')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
