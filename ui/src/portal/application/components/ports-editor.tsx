import { useState } from 'react'
import { useCertificates } from '@/portal/application/api'
import { ApplicationPortSpec } from '@/portal/application/types'
import { format } from 'date-fns'
import { useTranslation } from 'react-i18next'

import { hostCovered } from '@/lib/cert-coverage'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

interface PortsEditorProps {
  ports: ApplicationPortSpec[]
  ingressClass: string
  tlsCertificate: string
  onChange: (
    ports: ApplicationPortSpec[],
    ingressClass: string,
    tlsCertificate: string
  ) => void
}

const PORT_TYPES = ['clusterip', 'nodeport', 'loadbalancer'] as const

// shadcn Select rejects an empty-string item, so "disabled" rides a sentinel
// value (same trick as the deploy dialog's pull-credential picker).
const tlsCertificateNone = '__none__'

// Wizard step 4 (design §8): one row per port — port + Service type + optional
// external host/path. Service port names are auto-generated server side
// (tcp-<port>); container ports are not declared, the Service routes to the
// numeric port. A filled host renders an Ingress rule; IngressClass shows only
// when any row has a host.
export function PortsEditor({
  ports,
  ingressClass,
  tlsCertificate,
  onChange,
}: PortsEditorProps) {
  const { t } = useTranslation()
  const [openHostRows, setOpenHostRows] = useState<Record<number, boolean>>({})
  const { data: certificates } = useCertificates()

  // A tlsCertificate without any host fails spec validation, and hiding the
  // picker with the last host removed would leave it unfixable from the form.
  const tlsWithHost = (nextPorts: ApplicationPortSpec[]) =>
    nextPorts.some((port) => port.host?.trim()) ? tlsCertificate : ''

  const update = (index: number, updates: Partial<ApplicationPortSpec>) => {
    const next = ports.map((port, i) =>
      i === index ? { ...port, ...updates } : port
    )
    onChange(next, ingressClass, tlsWithHost(next))
  }

  const addPort = () => {
    onChange(
      [...ports, { port: 0, type: 'clusterip' }],
      ingressClass,
      tlsCertificate
    )
    setOpenHostRows((prev) => ({ ...prev, [ports.length]: false }))
  }

  const removePort = (index: number) => {
    const next = ports.filter((_, i) => i !== index)
    onChange(next, ingressClass, tlsWithHost(next))
  }

  const needsIngressClass = ports.some((port) => port.host?.trim())

  // Soft reminder: hosts the selected certificate does not cover. Advisory
  // only — a mismatched pair still saves and publishes.
  const selectedCert = (certificates ?? []).find(
    (cert) => cert.name === tlsCertificate
  )
  const uncoveredHosts =
    selectedCert !== undefined
      ? [
          ...new Set(
            ports
              .map((port) => port.host?.trim().toLowerCase())
              .filter((host): host is string => !!host)
          ),
        ].filter((host) => !hostCovered(host, selectedCert))
      : []

  return (
    <div className="rounded-lg border p-4">
      <div className="text-sm font-medium">
        {t('applications.wizard.ports')}
      </div>
      <p className="mt-1 text-xs text-muted-foreground">
        {t('applications.wizard.portsHint')}
      </p>

      <div className="mt-3 space-y-2">
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground md:flex-nowrap">
          <span className="w-24 shrink-0">{t('applications.wizard.port')}</span>
          <span className="w-36 shrink-0">
            {t('applications.wizard.portTypeLabel')}
          </span>
          <span className="min-w-32 flex-1">
            {t('applications.wizard.externalAccess')}
          </span>
          <span className="w-8 shrink-0" />
        </div>
        {ports.map((port, index) => {
          const hostOpen = openHostRows[index] || !!port.host
          return (
            <div
              key={index}
              className="flex flex-wrap items-center gap-2 md:flex-nowrap"
            >
              <Input
                value={port.port || ''}
                onChange={(event) =>
                  update(index, { port: Number(event.target.value) || 0 })
                }
                type="number"
                min={1}
                max={65535}
                placeholder="80"
                className="w-24 shrink-0"
              />
              <Select
                value={port.type || 'clusterip'}
                onValueChange={(value) => update(index, { type: value })}
              >
                <SelectTrigger className="w-36 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {PORT_TYPES.map((type) => (
                    <SelectItem key={type} value={type}>
                      {t(`applications.wizard.portType.${type}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {hostOpen ? (
                <>
                  <Input
                    value={port.host ?? ''}
                    onChange={(event) =>
                      update(index, { host: event.target.value })
                    }
                    placeholder={t('applications.wizard.hostPlaceholder')}
                    className="w-56 shrink-0 font-mono text-xs"
                  />
                  <Input
                    value={port.path ?? ''}
                    onChange={(event) =>
                      update(index, { path: event.target.value })
                    }
                    placeholder="/"
                    className="w-36 shrink-0 font-mono text-xs"
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="shrink-0 px-2 text-xs"
                    onClick={() => {
                      update(index, { host: '' })
                      setOpenHostRows((prev) => ({ ...prev, [index]: false }))
                    }}
                  >
                    {t('applications.wizard.removeHost')}
                  </Button>
                </>
              ) : (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="shrink-0 border-dashed text-xs text-muted-foreground"
                  onClick={() =>
                    setOpenHostRows((prev) => ({ ...prev, [index]: true }))
                  }
                >
                  ＋ {t('applications.wizard.addHost')}
                </Button>
              )}
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="w-8 shrink-0 px-0"
                title={t('applications.wizard.removePort')}
                onClick={() => removePort(index)}
              >
                ✕
              </Button>
            </div>
          )
        })}
      </div>
      <div className="mt-2 text-center">
        <button
          type="button"
          className="cursor-pointer text-xs text-muted-foreground transition-colors hover:text-foreground"
          onClick={addPort}
        >
          ＋ {t('applications.wizard.addPort')}
        </button>
      </div>

      {needsIngressClass && (
        <div className="mt-4 flex flex-wrap gap-4">
          <div className="w-56">
            <Label className="text-sm">
              {t('applications.wizard.ingressClass')}
            </Label>
            <Input
              value={ingressClass}
              onChange={(event) =>
                onChange(ports, event.target.value, tlsCertificate)
              }
              placeholder={t('applications.wizard.ingressClassPlaceholder')}
              className="mt-1.5"
            />
          </div>
          <div className="w-72">
            <Label className="text-sm">
              {t('applications.wizard.tlsCertificate')}
            </Label>
            <Select
              value={tlsCertificate || tlsCertificateNone}
              onValueChange={(value) =>
                onChange(
                  ports,
                  ingressClass,
                  value === tlsCertificateNone ? '' : value
                )
              }
            >
              <SelectTrigger className="mt-1.5 w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={tlsCertificateNone}>
                  {t('applications.wizard.tlsCertificatePlaceholder')}
                </SelectItem>
                {(certificates ?? []).map((cert) => (
                  <SelectItem key={cert.name} value={cert.name}>
                    {cert.name} · {cert.commonName || cert.dnsNames[0] || '—'} ·{' '}
                    {format(new Date(cert.notAfter), 'yyyy-MM-dd')}
                  </SelectItem>
                ))}
                {/* A spec naming a certificate that was deleted still shows
                    its raw name instead of a blank select (same echo trick as
                    the deploy dialog's pull-credential picker). */}
                {tlsCertificate !== '' && selectedCert === undefined && (
                  <SelectItem value={tlsCertificate}>
                    {tlsCertificate}
                  </SelectItem>
                )}
              </SelectContent>
            </Select>
            {uncoveredHosts.length > 0 && (
              <p className="mt-1 text-xs text-amber-600 dark:text-amber-400">
                {t('applications.wizard.tlsUncovered', {
                  hosts: uncoveredHosts.join(', '),
                })}
              </p>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
