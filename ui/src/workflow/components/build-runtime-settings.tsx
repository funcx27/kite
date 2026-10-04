// Build runtime settings: hosts / registry wiring of the buildfleet default
// runtime profile. First load shows the currently effective values (the API
// falls back to the config file when nothing is persisted); saving PUTs the
// whole wiring — docker / config-file providers only pick it up after a
// restart, hence the note on the card.
import '@/workflow/i18n'

import { useEffect, useState } from 'react'
import { IconPlus, IconTrash } from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'

import { useRuntimeSettings, useUpdateRuntimeSettingsMutation } from '../api'
import type { RuntimeSettings } from '../types'

// Editable rows; list-typed values stay comma-separated text until save.
interface HostRow {
  ip: string
  names: string
}

interface MirrorRow {
  host: string
  sources: string
}

interface RuntimeForm {
  hosts: HostRow[]
  insecure: string[]
  mirrors: MirrorRow[]
}

const emptyForm: RuntimeForm = { hosts: [], insecure: [], mirrors: [] }

const formOf = (s: RuntimeSettings): RuntimeForm => ({
  hosts: s.hosts.map((h) => ({ ip: h.ip, names: h.names.join(', ') })),
  insecure: [...s.insecureRegistries],
  mirrors: Object.entries(s.mirrors).map(([host, sources]) => ({
    host,
    sources: sources.join(', '),
  })),
})

const splitList = (raw: string) =>
  raw
    .split(',')
    .map((entry) => entry.trim())
    .filter((entry) => entry !== '')

export function BuildRuntimeSettings() {
  const { t } = useTranslation()

  const { data, isLoading, error } = useRuntimeSettings()
  const updateMutation = useUpdateRuntimeSettingsMutation()

  const [form, setForm] = useState<RuntimeForm>(emptyForm)

  // Sync the form whenever the query delivers a value we have not rendered
  // yet: first load, and refetched data after an external or own save.
  const [synced, setSynced] = useState<RuntimeSettings | null>(null)
  useEffect(() => {
    if (!data || data === synced) return
    setSynced(data)
    setForm(formOf(data))
  }, [data, synced])

  const save = () => {
    const hosts = []
    for (const row of form.hosts) {
      const ip = row.ip.trim()
      const names = splitList(row.names)
      if (ip === '' && names.length === 0) continue
      if (ip === '' || names.length === 0) {
        toast.error(t('workflow.buildRuntime.hostsRequired'))
        return
      }
      hosts.push({ ip, names })
    }
    const insecureRegistries: string[] = []
    for (const raw of form.insecure) {
      const entry = raw.trim()
      if (entry === '') continue
      if (!entry.startsWith('http://') && !entry.startsWith('https://')) {
        toast.error(t('workflow.buildRuntime.insecureInvalid'))
        return
      }
      insecureRegistries.push(entry)
    }
    const mirrors: Record<string, string[]> = {}
    for (const row of form.mirrors) {
      const host = row.host.trim()
      const sources = splitList(row.sources)
      if (host === '' && sources.length === 0) continue
      if (host === '' || sources.length === 0) {
        toast.error(t('workflow.buildRuntime.mirrorsRequired'))
        return
      }
      mirrors[host] = sources
    }
    updateMutation.mutate(
      { hosts, insecureRegistries, mirrors },
      {
        onSuccess: (saved) => {
          toast.success(t('workflow.buildRuntime.saved'))
          setSynced(saved)
          setForm(formOf(saved))
        },
        onError: (err) => toast.error(err.message),
      }
    )
  }

  const pending = updateMutation.isPending

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          {t('workflow.buildRuntime.title')}
          <Badge variant="secondary">{t('workflow.globalBadge')}</Badge>
        </CardTitle>
        <CardDescription>
          {t('workflow.buildRuntime.description')}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {error ? (
          <p className="text-sm text-muted-foreground">
            {t('workflow.buildRuntime.loadFailed')}
          </p>
        ) : (
          <div className="space-y-4">
            <div className="space-y-2">
              <Label className="text-xs font-semibold">
                {t('workflow.buildRuntime.hostsSection')}
              </Label>
              <p className="text-[11px] text-muted-foreground">
                {t('workflow.buildRuntime.hostsHint')}
              </p>
              {form.hosts.length > 0 && (
                <div className="grid grid-cols-[1fr_2fr_auto] gap-2">
                  <Label className="text-xs text-muted-foreground">
                    {t('workflow.buildRuntime.hostIp')}
                  </Label>
                  <Label className="text-xs text-muted-foreground">
                    {t('workflow.buildRuntime.hostNames')}
                  </Label>
                  <span />
                </div>
              )}
              {form.hosts.map((row, index) => (
                <div
                  key={index}
                  className="grid grid-cols-[1fr_2fr_auto] items-center gap-2"
                >
                  <Input
                    value={row.ip}
                    onChange={(e) =>
                      setForm({
                        ...form,
                        hosts: form.hosts.map((r, i) =>
                          i === index ? { ...r, ip: e.target.value } : r
                        ),
                      })
                    }
                    placeholder="10.0.0.5"
                    className="font-mono text-xs"
                  />
                  <Input
                    value={row.names}
                    onChange={(e) =>
                      setForm({
                        ...form,
                        hosts: form.hosts.map((r, i) =>
                          i === index ? { ...r, names: e.target.value } : r
                        ),
                      })
                    }
                    placeholder="registry.example.com, git.example.com"
                    className="font-mono text-xs"
                  />
                  <Button
                    variant="ghost"
                    size="icon"
                    className="text-muted-foreground hover:text-destructive"
                    onClick={() =>
                      setForm({
                        ...form,
                        hosts: form.hosts.filter((_, i) => i !== index),
                      })
                    }
                  >
                    <IconTrash className="h-4 w-4" />
                  </Button>
                </div>
              ))}
              <Button
                variant="outline"
                size="sm"
                onClick={() =>
                  setForm({
                    ...form,
                    hosts: [...form.hosts, { ip: '', names: '' }],
                  })
                }
              >
                <IconPlus className="mr-1 h-3.5 w-3.5" />
                {t('workflow.buildRuntime.addHost')}
              </Button>
            </div>

            <Separator />

            <div className="space-y-2">
              <Label className="text-xs font-semibold">
                {t('workflow.buildRuntime.insecureSection')}
              </Label>
              {form.insecure.map((entry, index) => (
                <div key={index} className="flex items-center gap-2">
                  <Input
                    value={entry}
                    onChange={(e) =>
                      setForm({
                        ...form,
                        insecure: form.insecure.map((r, i) =>
                          i === index ? e.target.value : r
                        ),
                      })
                    }
                    placeholder="http://registry.local:5000"
                    className="font-mono text-xs"
                  />
                  <Button
                    variant="ghost"
                    size="icon"
                    className="text-muted-foreground hover:text-destructive"
                    onClick={() =>
                      setForm({
                        ...form,
                        insecure: form.insecure.filter((_, i) => i !== index),
                      })
                    }
                  >
                    <IconTrash className="h-4 w-4" />
                  </Button>
                </div>
              ))}
              <div className="flex items-center justify-between">
                <p className="text-[11px] text-muted-foreground">
                  {t('workflow.buildRuntime.insecureHint')}
                </p>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    setForm({ ...form, insecure: [...form.insecure, ''] })
                  }
                >
                  <IconPlus className="mr-1 h-3.5 w-3.5" />
                  {t('workflow.buildRuntime.addInsecure')}
                </Button>
              </div>
            </div>

            <Separator />

            <div className="space-y-2">
              <Label className="text-xs font-semibold">
                {t('workflow.buildRuntime.mirrorsSection')}
              </Label>
              {form.mirrors.length > 0 && (
                <div className="grid grid-cols-[1fr_2fr_auto] gap-2">
                  <Label className="text-xs text-muted-foreground">
                    {t('workflow.buildRuntime.mirrorHost')}
                  </Label>
                  <Label className="text-xs text-muted-foreground">
                    {t('workflow.buildRuntime.mirrorSources')}
                  </Label>
                  <span />
                </div>
              )}
              {form.mirrors.map((row, index) => (
                <div
                  key={index}
                  className="grid grid-cols-[1fr_2fr_auto] items-center gap-2"
                >
                  <Input
                    value={row.host}
                    onChange={(e) =>
                      setForm({
                        ...form,
                        mirrors: form.mirrors.map((r, i) =>
                          i === index ? { ...r, host: e.target.value } : r
                        ),
                      })
                    }
                    placeholder="docker.io"
                    className="font-mono text-xs"
                  />
                  <Input
                    value={row.sources}
                    onChange={(e) =>
                      setForm({
                        ...form,
                        mirrors: form.mirrors.map((r, i) =>
                          i === index ? { ...r, sources: e.target.value } : r
                        ),
                      })
                    }
                    placeholder="mirror-registry.local:5000/library"
                    className="font-mono text-xs"
                  />
                  <Button
                    variant="ghost"
                    size="icon"
                    className="text-muted-foreground hover:text-destructive"
                    onClick={() =>
                      setForm({
                        ...form,
                        mirrors: form.mirrors.filter((_, i) => i !== index),
                      })
                    }
                  >
                    <IconTrash className="h-4 w-4" />
                  </Button>
                </div>
              ))}
              <Button
                variant="outline"
                size="sm"
                onClick={() =>
                  setForm({
                    ...form,
                    mirrors: [...form.mirrors, { host: '', sources: '' }],
                  })
                }
              >
                <IconPlus className="mr-1 h-3.5 w-3.5" />
                {t('workflow.buildRuntime.addMirror')}
              </Button>
            </div>

            <Separator />

            <div className="flex flex-wrap items-center justify-between gap-2">
              <p className="text-xs text-muted-foreground">
                {t('workflow.buildRuntime.restartNote')}
              </p>
              <Button size="sm" disabled={pending} onClick={save}>
                {t('common.actions.save')}
              </Button>
            </div>
            {isLoading && (
              <p className="text-sm text-muted-foreground">
                {t('common.messages.loading')}
              </p>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
