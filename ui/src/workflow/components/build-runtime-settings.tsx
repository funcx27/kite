// Build runtime settings: BuildKit images / hosts / registry wiring of the
// buildfleet default runtime profile. The card shows the effective summary;
// all fields are edited in a dialog. Saving PUTs the whole wiring — the
// docker providers' shared buildkitd.toml is written once at startup and
// config-file providers never remount, hence the restart note.
import '@/workflow/i18n'

import { useEffect, useState } from 'react'
import { IconPencil, IconPlus, IconTrash } from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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
  rootfulImage: string
  rootlessImage: string
  hosts: HostRow[]
  insecure: string[]
  mirrors: MirrorRow[]
}

const emptyForm: RuntimeForm = {
  rootfulImage: '',
  rootlessImage: '',
  hosts: [],
  insecure: [],
  mirrors: [],
}

const formOf = (s: RuntimeSettings): RuntimeForm => ({
  // ?? '' tolerates a pre-upgrade backend that does not return the image fields.
  rootfulImage: s.rootfulImage ?? '',
  rootlessImage: s.rootlessImage ?? '',
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
  const [dialogOpen, setDialogOpen] = useState(false)

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
      {
        rootfulImage: form.rootfulImage.trim(),
        rootlessImage: form.rootlessImage.trim(),
        hosts,
        insecureRegistries,
        mirrors,
      },
      {
        onSuccess: (saved) => {
          toast.success(t('workflow.buildRuntime.saved'))
          setSynced(saved)
          setForm(formOf(saved))
          setDialogOpen(false)
        },
        onError: (err) => toast.error(err.message),
      }
    )
  }

  const pending = updateMutation.isPending
  const builtin = t('workflow.buildRuntime.builtinDefault')

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between space-y-0">
        <div className="space-y-1.5">
          <CardTitle className="flex items-center gap-2">
            {t('workflow.buildRuntime.title')}
          </CardTitle>
          <CardDescription>
            {t('workflow.buildRuntime.description')}
          </CardDescription>
        </div>
        <Button
          size="icon"
          disabled={isLoading || error !== null}
          onClick={() => setDialogOpen(true)}
          title={t('workflow.buildRuntime.editTitle')}
        >
          <IconPencil className="h-4 w-4" />
        </Button>
      </CardHeader>
      <CardContent>
        {error ? (
          <p className="text-sm text-muted-foreground">
            {t('workflow.buildRuntime.loadFailed')}
          </p>
        ) : (
          <div className="space-y-1 text-sm">
            <p className="truncate">
              <span className="text-muted-foreground">
                {t('workflow.buildRuntime.rootfulImage')}:{' '}
              </span>
              <span className="font-mono text-xs">
                {data?.rootfulImage || builtin}
              </span>
            </p>
            <p className="truncate">
              <span className="text-muted-foreground">
                {t('workflow.buildRuntime.rootlessImage')}:{' '}
              </span>
              <span className="font-mono text-xs">
                {data?.rootlessImage || builtin}
              </span>
            </p>
            <p className="text-muted-foreground">
              {t('workflow.buildRuntime.summaryCounts', {
                hosts: data?.hosts.length ?? 0,
                insecure: data?.insecureRegistries.length ?? 0,
                mirrors: Object.keys(data?.mirrors ?? {}).length,
              })}
            </p>
            <p className="text-xs text-muted-foreground">
              {t('workflow.buildRuntime.restartNote')}
            </p>
            {isLoading && (
              <p className="text-sm text-muted-foreground">
                {t('common.messages.loading')}
              </p>
            )}
          </div>
        )}
      </CardContent>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t('workflow.buildRuntime.editTitle')}</DialogTitle>
          </DialogHeader>
          <div className="max-h-[60vh] space-y-4 overflow-y-auto pr-1">
            <div className="space-y-2">
              <Label className="text-xs font-semibold">
                {t('workflow.buildRuntime.imagesSection')}
              </Label>
              <div className="space-y-1.5">
                <div className="flex items-center gap-3">
                  <Label className="w-32 shrink-0 text-xs">
                    {t('workflow.buildRuntime.rootfulImage')}
                  </Label>
                  <Input
                    value={form.rootfulImage}
                    onChange={(e) =>
                      setForm({ ...form, rootfulImage: e.target.value })
                    }
                    placeholder="moby/buildkit:master"
                    className="font-mono text-xs"
                  />
                </div>
                <div className="flex items-center gap-3">
                  <Label className="w-32 shrink-0 text-xs">
                    {t('workflow.buildRuntime.rootlessImage')}
                  </Label>
                  <Input
                    value={form.rootlessImage}
                    onChange={(e) =>
                      setForm({ ...form, rootlessImage: e.target.value })
                    }
                    placeholder="moby/buildkit:master-rootless"
                    className="font-mono text-xs"
                  />
                </div>
              </div>
              <p className="text-[11px] text-muted-foreground">
                {t('workflow.buildRuntime.imagesHint')}
              </p>
            </div>

            <Separator />

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
                size="icon"
                title={t('workflow.buildRuntime.addHost')}
                onClick={() =>
                  setForm({
                    ...form,
                    hosts: [...form.hosts, { ip: '', names: '' }],
                  })
                }
              >
                <IconPlus className="h-4 w-4" />
              </Button>
            </div>

            <Separator />

            <div className="space-y-2">
              <Label className="text-xs font-semibold">
                {t('workflow.buildRuntime.insecureSection')}
              </Label>
              <p className="text-[11px] text-muted-foreground">
                {t('workflow.buildRuntime.insecureHint')}
              </p>
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
              <Button
                variant="outline"
                size="icon"
                title={t('workflow.buildRuntime.addInsecure')}
                onClick={() =>
                  setForm({ ...form, insecure: [...form.insecure, ''] })
                }
              >
                <IconPlus className="h-4 w-4" />
              </Button>
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
                size="icon"
                title={t('workflow.buildRuntime.addMirror')}
                onClick={() =>
                  setForm({
                    ...form,
                    mirrors: [...form.mirrors, { host: '', sources: '' }],
                  })
                }
              >
                <IconPlus className="h-4 w-4" />
              </Button>
            </div>
          </div>
          <DialogFooter>
            <p className="mr-auto max-w-[60%] text-xs text-muted-foreground">
              {t('workflow.buildRuntime.restartNote')}
            </p>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              {t('common.actions.cancel')}
            </Button>
            <Button disabled={pending} onClick={save}>
              {t('common.actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}
