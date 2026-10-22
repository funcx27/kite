import { useMemo, useState } from 'react'
import { useCertificates } from '@/portal/application/api'
import {
  useAnnotationKeys,
  useCreateHostMutation,
  useNetworkIngressClasses,
  useReplaceRoutesMutation,
  type NetworkRouteInput,
} from '@/portal/network/api'
import { catalogEntryMatches } from '@/portal/network/ingress-family'
import * as yaml from 'js-yaml'
import { Check, Upload, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

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
import { Textarea } from '@/components/ui/textarea'

// Radix Select items cannot carry an empty value; sentinel marks "no cert".
const CERT_NONE = '__none__'

type ImportRoute = NetworkRouteInput

interface ImportHost {
  host: string
  ingressClass: string
  routes: ImportRoute[]
}

interface InvalidDoc {
  index: number
  // i18n key suffix under network.import.reason.
  reason: string
}

interface ParseResult {
  hosts: ImportHost[]
  invalid: InvalidDoc[]
  skippedAnnotations: string[]
}

interface ImportResultRow {
  host: string
  ok: boolean
  error?: string
}

interface NetworkImportDialogProps {
  space: string
  clusterNames: string[]
  defaultCluster: string
  onClose: () => void
}

// One Ingress document → host route-table fragment; returns the first
// validation failure or null when the document is usable.
function validateDoc(doc: Record<string, unknown>): string | null {
  const spec = doc.spec as Record<string, unknown> | undefined
  if (typeof doc.apiVersion !== 'string' || doc.kind !== 'Ingress') {
    return 'invalidKind'
  }
  const metadata = doc.metadata as Record<string, unknown> | undefined
  if (typeof metadata?.name !== 'string' || metadata.name === '') {
    return 'invalidName'
  }
  if (typeof spec?.ingressClassName !== 'string' || !spec.ingressClassName) {
    return 'invalidClass'
  }
  const rules = spec?.rules
  if (!Array.isArray(rules) || rules.length === 0) {
    return 'invalidRules'
  }
  // A host-less rule (standard catch-all Ingress) is acceptable when the TLS
  // section names the domain; otherwise there is nothing to derive a host from.
  const tls = spec?.tls as Array<{ hosts?: unknown }> | undefined
  const tlsHosts = Array.isArray(tls?.[0]?.hosts)
    ? (tls[0].hosts as unknown[])
    : []
  for (const rule of rules) {
    const r = rule as Record<string, unknown>
    const hostEmpty = typeof r.host !== 'string' || r.host === ''
    if (hostEmpty && tlsHosts.length === 0) return 'invalidHost'
    const paths = (r.http as { paths?: unknown } | undefined)?.paths
    if (!Array.isArray(paths) || paths.length === 0) return 'invalidRules'
    for (const path of paths) {
      const p = path as Record<string, unknown>
      if (typeof p.path !== 'string' || !p.path.startsWith('/')) {
        return 'invalidPath'
      }
      const backend = p.backend as Record<string, unknown> | undefined
      const service = backend?.service as Record<string, unknown> | undefined
      const servicePort = service?.port as { number?: unknown } | undefined
      if (
        typeof service?.name !== 'string' ||
        service.name === '' ||
        typeof servicePort?.number !== 'number'
      ) {
        return 'invalidBackend'
      }
    }
  }
  return null
}

// Parse + validate the pasted multi-document stream and group the valid
// documents by rules[].host. Annotations of annotated documents spread onto
// every service they touch, filtered to the platform catalog (class or
// family); catalog-foreign keys are skipped and reported.
function parseIngressDocs(
  raw: string,
  catalogKeys: Array<{ ingressClass: string; key: string }>,
  classes: Array<{ name: string; controller: string }>,
  t: (key: string) => string
): ParseResult {
  const result: ParseResult = {
    hosts: [],
    invalid: [],
    skippedAnnotations: [],
  }
  let loaded: unknown[]
  try {
    loaded = yaml.loadAll(raw)
  } catch {
    return {
      hosts: [],
      invalid: [{ index: 0, reason: t('network.import.reason.invalidYaml') }],
      skippedAnnotations: [],
    }
  }
  const docs = loaded.filter((doc) => doc != null)
  if (docs.length === 0) {
    return {
      hosts: [],
      invalid: [{ index: 0, reason: t('network.import.reason.noDocs') }],
      skippedAnnotations: [],
    }
  }

  const byHost = new Map<string, ImportHost>()
  docs.forEach((loadedDoc, index) => {
    if (
      typeof loadedDoc !== 'object' ||
      Array.isArray(loadedDoc) ||
      loadedDoc === null
    ) {
      result.invalid.push({
        index,
        reason: t('network.import.reason.invalidKind'),
      })
      return
    }
    const doc = loadedDoc as Record<string, unknown>
    const reason = validateDoc(doc)
    if (reason !== null) {
      result.invalid.push({
        index,
        reason: t(`network.import.reason.${reason}`),
      })
      return
    }
    const spec = doc.spec as {
      ingressClassName: string
      tls?: Array<{ hosts?: string[] }>
      rules: Array<{
        host?: string
        http?: {
          paths?: Array<{
            path: string
            backend?: {
              service?: { name?: string; port?: { number?: number } }
            }
          }>
        }
      }>
    }
    const metadata = doc.metadata as {
      annotations?: Record<string, string>
    }

    // Catalog whitelist for this document's class; annotation keys outside
    // it are skipped and surfaced once in the preview.
    let routeAnnotations: Record<string, string> | undefined
    if (metadata.annotations) {
      const filtered: Record<string, string> = {}
      for (const [key, value] of Object.entries(metadata.annotations)) {
        const matchesCatalog = catalogKeys.some(
          (entry) =>
            entry.key === key &&
            catalogEntryMatches(
              entry.ingressClass,
              spec.ingressClassName,
              classes
            )
        )
        if (matchesCatalog) {
          filtered[key] = String(value)
        } else if (!result.skippedAnnotations.includes(key)) {
          result.skippedAnnotations.push(key)
        }
      }
      if (Object.keys(filtered).length > 0) routeAnnotations = filtered
    }

    const tlsFallback = spec.tls?.[0]?.hosts?.[0] ?? ''
    for (const rule of spec.rules) {
      const hostName = rule.host || tlsFallback
      if (!hostName) continue
      let target = byHost.get(hostName)
      if (!target) {
        target = {
          host: hostName,
          ingressClass: spec.ingressClassName,
          routes: [],
        }
        byHost.set(hostName, target)
        result.hosts.push(target)
      }
      const seen = new Set(target.routes.map((route) => route.path))
      for (const path of rule.http?.paths ?? []) {
        if (seen.has(path.path)) continue
        seen.add(path.path)
        target.routes.push({
          path: path.path,
          appName: path.backend?.service?.name as string,
          port: path.backend?.service?.port?.number as number,
          annotations: routeAnnotations,
        })
      }
    }
  })
  return result
}

// Network host import: paste or upload Ingress YAML, preview the hosts the
// documents expand to (grouped by rules[].host), pick cluster + certificate,
// then create one host per group with its route table. Certificates and TLS
// sections in the documents are not imported.
export function NetworkImportDialog({
  space,
  clusterNames,
  defaultCluster,
  onClose,
}: NetworkImportDialogProps) {
  const { t } = useTranslation()
  const createMutation = useCreateHostMutation()
  const replaceRoutesMutation = useReplaceRoutesMutation()
  const { data: certificates } = useCertificates()
  const [content, setContent] = useState('')
  const [cluster, setCluster] = useState(defaultCluster)
  const [cert, setCert] = useState(CERT_NONE)
  const [importing, setImporting] = useState(false)
  const [results, setResults] = useState<ImportResultRow[] | null>(null)

  const classesQuery = useNetworkIngressClasses(cluster)
  const catalogQuery = useAnnotationKeys()
  const ingressClasses = classesQuery.data
  const catalogData = catalogQuery.data

  const parsed = useMemo(
    () =>
      parseIngressDocs(
        content,
        catalogData ?? [],
        ingressClasses ?? [],
        (key) => t(key)
      ),
    [content, catalogData, ingressClasses, t]
  )

  const handleFile = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (!file) return
    try {
      setContent(await file.text())
    } catch {
      toast.error(t('network.import.fileReadError'))
    }
  }

  // The catalog whitelist depends on the annotation catalog and the selected
  // cluster's IngressClass list (for family matching); placeholderData keeps
  // the previous cluster's classes mounted during a key switch, so the
  // current key must have actually landed before importing.
  const referencesLoaded =
    catalogData !== undefined &&
    ingressClasses !== undefined &&
    !classesQuery.isPlaceholderData

  const canImport =
    !!space &&
    !!cluster &&
    referencesLoaded &&
    parsed.hosts.length > 0 &&
    parsed.invalid.length === 0 &&
    !importing

  const runImport = async () => {
    if (!canImport) return
    setImporting(true)
    setResults(null)
    const collected: ImportResultRow[] = []
    for (const target of parsed.hosts) {
      try {
        await createMutation.mutateAsync({
          space,
          req: {
            host: target.host.toLowerCase(),
            cluster,
            ingressClass: target.ingressClass,
            tlsCertificate: cert === CERT_NONE ? undefined : cert,
          },
        })
        await replaceRoutesMutation.mutateAsync({
          space,
          cluster,
          host: target.host.toLowerCase(),
          routes: target.routes,
        })
        collected.push({ host: target.host, ok: true })
      } catch (err) {
        collected.push({
          host: target.host,
          ok: false,
          error: err instanceof Error ? err.message : String(err),
        })
      }
    }
    setResults(collected)
    setImporting(false)
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="flex max-h-[calc(100dvh-2rem)] flex-col overflow-hidden sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Upload className="size-4" />
            {t('network.import.title')}
          </DialogTitle>
          <DialogDescription>
            {t('network.import.description')}
          </DialogDescription>
        </DialogHeader>
        <div className="flex-1 space-y-3 overflow-y-auto">
          <div className="flex items-center gap-3">
            <Input
              type="file"
              accept=".yaml,.yml,text/yaml"
              className="h-9 flex-1"
              onChange={(event) => void handleFile(event)}
            />
          </div>
          <Textarea
            value={content}
            onChange={(event) => setContent(event.target.value)}
            placeholder={t('network.import.contentPlaceholder')}
            className="min-h-40 font-mono text-xs"
          />

          {parsed.invalid.length > 0 && (
            <div className="space-y-1">
              {parsed.invalid.map((entry) => (
                <p
                  key={entry.index}
                  className="flex items-start gap-2 text-xs text-destructive"
                >
                  <X className="mt-0.5 size-3.5 shrink-0" />
                  <span>
                    #{entry.index + 1} {entry.reason}
                  </span>
                </p>
              ))}
            </div>
          )}

          {parsed.hosts.length > 0 && (
            <div className="space-y-1">
              {parsed.hosts.map((target) => (
                <div
                  key={target.host}
                  className="flex flex-wrap items-center gap-2 rounded-md border px-2.5 py-1.5 text-xs"
                >
                  <Check className="size-3.5 shrink-0 text-green-600" />
                  <span className="font-mono font-medium">{target.host}</span>
                  <span className="text-muted-foreground">
                    {target.ingressClass} ·{' '}
                    {t('network.import.routeCount', {
                      count: target.routes.length,
                    })}
                  </span>
                </div>
              ))}
              {parsed.skippedAnnotations.length > 0 && (
                <p className="text-xs text-amber-600 dark:text-amber-400">
                  {t('network.import.skippedAnnotations', {
                    keys: parsed.skippedAnnotations.join(', '),
                  })}
                </p>
              )}
            </div>
          )}

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label>{t('network.import.cluster')}</Label>
              <Select
                value={cluster}
                onValueChange={setCluster}
                disabled={importing}
              >
                <SelectTrigger className="w-full font-mono">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {clusterNames.map((name) => (
                    <SelectItem key={name} value={name} className="font-mono">
                      {name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label>{t('network.import.certificate')}</Label>
              <Select value={cert} onValueChange={setCert} disabled={importing}>
                <SelectTrigger className="w-full font-mono">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={CERT_NONE}>
                    {t('network.import.certNone')}
                  </SelectItem>
                  {(certificates ?? []).map((certificate) => (
                    <SelectItem key={certificate.name} value={certificate.name}>
                      {certificate.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          {results && (
            <div className="space-y-1">
              {results.map((row) => (
                <div
                  key={row.host}
                  className="flex items-start gap-2 rounded-md border px-2.5 py-1.5 text-xs"
                >
                  {row.ok ? (
                    <Check className="mt-0.5 size-3.5 shrink-0 text-green-600" />
                  ) : (
                    <X className="mt-0.5 size-3.5 shrink-0 text-destructive" />
                  )}
                  <span className="font-mono">{row.host}</span>
                  {row.ok ? (
                    <span className="text-muted-foreground">
                      {t('network.import.resultCreated')}
                    </span>
                  ) : (
                    <span className="min-w-0 break-all text-destructive">
                      {row.error}
                    </span>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={importing}>
            {t('network.import.cancel')}
          </Button>
          <Button disabled={!canImport} onClick={() => void runImport()}>
            {importing
              ? t('network.import.importing')
              : t('network.import.import')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
