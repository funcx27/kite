// Build runtime settings: BuildKit images / hosts / registry wiring of the
// buildfleet default runtime profile. The card shows the effective summary;
// all fields are edited in a dialog. Saving PUTs the whole wiring — the
// docker providers' shared buildkitd.toml is written once at startup and
// config-file providers never remount, hence the restart note.
import '@/workflow/i18n'

import { useEffect, useState, type ReactNode } from 'react'
import {
  IconCopy,
  IconInfoCircle,
  IconPencil,
  IconTrash,
} from '@tabler/icons-react'
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

// Registry endpoint: scheme + bare host[:port]. buildkit joins anything
// after the authority onto the upstream path (e.g. /library → doubled), so
// trailing paths must fail validation here.
const REGISTRY_URL_RE =
  /^https?:\/\/[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?(?::\d{1,5})?$/

// Real-time validation helpers: empty (sentinel) rows stay neutral.
const insecureEntryInvalid = (entry: string) => {
  const v = entry.trim()
  return v !== '' && !REGISTRY_URL_RE.test(v)
}
const mirrorSourcesInvalid = (sources: string) =>
  splitList(sources).some((s) => !REGISTRY_URL_RE.test(s))
const HOST_IP_RE =
  /^((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)$/
const hostIpInvalid = (ip: string) => {
  const v = ip.trim()
  return v !== '' && !HOST_IP_RE.test(v)
}

// Mirror the backend's built-in buildkitd images (buildfleet config.Image)
// so the summary shows the effective reference when nothing is configured.
const BUILTIN_ROOTFUL_IMAGE = 'moby/buildkit:master'
const BUILTIN_ROOTLESS_IMAGE = 'moby/buildkit:master-rootless'

// Dialog fields rest as plain text: the border appears as a dashed hint on
// hover and solid on focus, signalling editability only on intent. Invalid
// rows get a solid destructive border at rest (the base border-none would
// otherwise hide the aria-invalid styling).
const INLINE_FIELD_CLS =
  'border-none px-1.5 font-mono text-xs shadow-none hover:border-dashed hover:border-input/60 focus-visible:border-solid aria-invalid:border-solid dark:bg-transparent!'

// Empty fields keep the ordinary bordered look — a visible spot to type
// into; the plain-text rest state only applies once there is content.
const fieldCls = (value: string) =>
  value === '' ? 'font-mono text-xs' : INLINE_FIELD_CLS

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

  // Keep one empty row at the tail of each editable list: filling the last
  // row appends the next one automatically. Empty rows are skipped on save,
  // so the sentinel never reaches the backend.
  useEffect(() => {
    const patch: Partial<RuntimeForm> = {}
    const lastHost = form.hosts[form.hosts.length - 1]
    if (!lastHost || lastHost.ip !== '' || lastHost.names !== '') {
      patch.hosts = [...form.hosts, { ip: '', names: '' }]
    }
    if (
      !form.insecure.length ||
      form.insecure[form.insecure.length - 1] !== ''
    ) {
      patch.insecure = [...form.insecure, '']
    }
    const lastMirror = form.mirrors[form.mirrors.length - 1]
    if (!lastMirror || lastMirror.host !== '' || lastMirror.sources !== '') {
      patch.mirrors = [...form.mirrors, { host: '', sources: '' }]
    }
    if (Object.keys(patch).length > 0) setForm({ ...form, ...patch })
  }, [form])

  // A plain-HTTP mirror source needs the matching insecure-registry entry
  // (buildkitd http=true block) to be reachable, so list them together:
  // on blur of a mirror row, add its sources to the insecure list. Manual
  // deletion sticks — re-adding only happens after editing the row again.
  // Additions replace the tail sentinel row, never land behind it (which
  // would strand an empty row between filled entries).
  const syncInsecureFromMirrors = () => {
    setForm((f) => {
      const have = new Set(f.insecure.map((e) => e.trim()).filter(Boolean))
      const additions: string[] = []
      for (const row of f.mirrors) {
        for (const raw of splitList(row.sources)) {
          if (REGISTRY_URL_RE.test(raw) && !have.has(raw)) {
            have.add(raw)
            additions.push(raw)
          }
        }
      }
      if (additions.length === 0) return f
      const filled = f.insecure.filter((e) => e.trim() !== '')
      return { ...f, insecure: [...filled, ...additions, ''] }
    })
  }

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
      if (hostIpInvalid(ip)) {
        toast.error(t('workflow.buildRuntime.hostIpInvalid', { ip }))
        return
      }
      hosts.push({ ip, names })
    }
    const insecureRegistries: string[] = []
    for (const raw of form.insecure) {
      const entry = raw.trim()
      if (entry === '') continue
      if (!REGISTRY_URL_RE.test(entry)) {
        toast.error(t('workflow.buildRuntime.insecureInvalid', { entry }))
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
      const badSource = sources.find((s) => !REGISTRY_URL_RE.test(s))
      if (badSource !== undefined) {
        toast.error(
          t('workflow.buildRuntime.mirrorInvalid', { source: badSource })
        )
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

  // Real-time regex state: with an invalid row the save button locks until
  // the offending value is fixed or cleared (save() re-checks defensively).
  const hasInvalid =
    form.hosts.some((row) => hostIpInvalid(row.ip)) ||
    form.insecure.some(insecureEntryInvalid) ||
    form.mirrors.some((row) => mirrorSourcesInvalid(row.sources))

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
          onClick={() => {
            // Fresh copy of the saved settings each time: cancel or abandon
            // must not leak earlier edits back into the dialog.
            if (data) setForm(formOf(data))
            setDialogOpen(true)
          }}
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
        ) : isLoading && !data ? (
          <p className="text-sm text-muted-foreground">
            {t('common.messages.loading')}
          </p>
        ) : (
          <div className="space-y-4">
            <div className="grid gap-3 sm:grid-cols-2">
              <ImageCard
                title={t('workflow.buildRuntime.rootfulTitle')}
                dotClassName="bg-primary"
                image={data?.rootfulImage || BUILTIN_ROOTFUL_IMAGE}
                custom={Boolean(data?.rootfulImage)}
                mode={t('workflow.buildRuntime.chipPrivileged')}
                scenario={t('workflow.buildRuntime.chipCompat')}
              />
              <ImageCard
                title={t('workflow.buildRuntime.rootlessTitle')}
                dotClassName="bg-emerald-500"
                image={data?.rootlessImage || BUILTIN_ROOTLESS_IMAGE}
                custom={Boolean(data?.rootlessImage)}
                mode={t('workflow.buildRuntime.chipUnprivileged')}
                scenario={t('workflow.buildRuntime.chipIsolation')}
              />
            </div>
            <div className="divide-y rounded-lg border text-xs">
              <ConfigPreview
                label={t('workflow.buildRuntime.hostsSection')}
                entries={(data?.hosts ?? []).map(
                  (h) => `${h.ip} → ${h.names.join(', ')}`
                )}
              />
              <ConfigPreview
                label={t('workflow.buildRuntime.insecureSection')}
                entries={data?.insecureRegistries ?? []}
              />
              <ConfigPreview
                label={t('workflow.buildRuntime.mirrorsSection')}
                entries={Object.entries(data?.mirrors ?? {}).map(
                  ([host, sources]) => `${host} → ${sources.join(', ')}`
                )}
              />
            </div>
            <div className="flex items-start gap-1.5 text-xs text-muted-foreground">
              <IconInfoCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              {t('workflow.buildRuntime.restartNote')}
            </div>
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
                    className={fieldCls(form.rootfulImage)}
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
                    className={fieldCls(form.rootlessImage)}
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
                    className={fieldCls(row.ip)}
                    aria-invalid={hostIpInvalid(row.ip)}
                    title={
                      hostIpInvalid(row.ip)
                        ? t('workflow.buildRuntime.hostIpInvalid', {
                            ip: row.ip.trim(),
                          })
                        : undefined
                    }
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
                    className={fieldCls(row.names)}
                  />
                  {(row.ip !== '' || row.names !== '') && (
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
                  )}
                </div>
              ))}
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
                    aria-invalid={insecureEntryInvalid(entry)}
                    title={
                      insecureEntryInvalid(entry)
                        ? t('workflow.buildRuntime.insecureInvalid', {
                            entry: entry.trim(),
                          })
                        : undefined
                    }
                    className={fieldCls(entry)}
                  />
                  {entry !== '' && (
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
                  )}
                </div>
              ))}
            </div>

            <Separator />

            <div className="space-y-2">
              <Label className="text-xs font-semibold">
                {t('workflow.buildRuntime.mirrorsSection')}
              </Label>
              <p className="text-[11px] text-muted-foreground">
                {t('workflow.buildRuntime.mirrorsHint')}
              </p>
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
                    className={fieldCls(row.host)}
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
                    className={fieldCls(row.sources)}
                    aria-invalid={mirrorSourcesInvalid(row.sources)}
                    title={
                      mirrorSourcesInvalid(row.sources)
                        ? t('workflow.buildRuntime.mirrorInvalid', {
                            source:
                              splitList(row.sources).find(
                                (s) => !REGISTRY_URL_RE.test(s)
                              ) ?? '',
                          })
                        : undefined
                    }
                    onBlur={syncInsecureFromMirrors}
                  />
                  {(row.host !== '' || row.sources !== '') && (
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
                  )}
                </div>
              ))}
            </div>
          </div>
          <DialogFooter>
            <p className="mr-auto max-w-[60%] text-xs text-muted-foreground">
              {t('workflow.buildRuntime.restartNote')}
            </p>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              {t('common.actions.cancel')}
            </Button>
            <Button disabled={pending || hasInvalid} onClick={save}>
              {t('common.actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}

// ImageCard summarizes one build mode's effective buildkitd image with a
// copy affordance and the mode's run/scenario chips.
function ImageCard({
  title,
  dotClassName,
  image,
  custom,
  mode,
  scenario,
}: {
  title: string
  dotClassName: string
  image: string
  custom: boolean
  mode: string
  scenario: string
}) {
  const { t } = useTranslation()

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(image)
      toast.success(t('workflow.buildRuntime.copied'))
    } catch {
      // Clipboard unavailable (insecure context): surface the reference so
      // it can still be selected and copied by hand.
      toast.info(image)
    }
  }

  return (
    <div className="rounded-lg border p-3.5">
      <div className="mb-2.5 flex items-center justify-between gap-2">
        <span className="flex items-center gap-2 text-sm font-semibold">
          <span className={`size-1.5 shrink-0 rounded-full ${dotClassName}`} />
          {title}
        </span>
        {!custom && (
          <Badge variant="secondary" className="font-normal">
            {t('workflow.buildRuntime.builtinDefault')}
          </Badge>
        )}
      </div>
      <div className="flex items-start gap-1.5">
        <code className="min-w-0 flex-1 break-all text-xs leading-5">
          {image}
        </code>
        <Button
          variant="ghost"
          size="icon"
          className="h-6 w-6 shrink-0 text-muted-foreground"
          onClick={copy}
          title={t('workflow.buildRuntime.copy')}
        >
          <IconCopy className="h-3.5 w-3.5" />
        </Button>
      </div>
      <div className="mt-3 flex flex-wrap gap-1.5">
        <MetaChip label={t('workflow.buildRuntime.chipMode')} value={mode} />
        <MetaChip
          label={t('workflow.buildRuntime.chipScenario')}
          value={scenario}
        />
      </div>
    </div>
  )
}

function MetaChip({ label, value }: { label: string; value: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-md border bg-background px-2 py-1 text-[11px] text-muted-foreground">
      {label}
      <b className="font-semibold text-foreground">{value}</b>
    </span>
  )
}

// ConfigPreview lists one settings section's saved values as read-only text;
// editing stays behind the edit dialog.
function ConfigPreview({
  label,
  entries,
}: {
  label: string
  entries: string[]
}) {
  const { t } = useTranslation()
  return (
    <div className="p-3">
      <div className="mb-1 font-medium text-muted-foreground">{label}</div>
      {entries.length === 0 ? (
        <div className="text-muted-foreground/70">
          {t('workflow.buildRuntime.noneConfigured')}
        </div>
      ) : (
        <ul className="space-y-0.5">
          {entries.map((entry) => (
            <li key={entry} className="break-all font-mono leading-5">
              {entry}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
