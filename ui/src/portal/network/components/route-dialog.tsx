import { useEffect, useMemo, useState } from 'react'
import {
  useAnnotationKeys,
  useNetworkIngressClasses,
  useNetworkServices,
  useReplaceRoutesMutation,
} from '@/portal/network/api'
import {
  AnnotationRows,
  type AnnotationRow,
} from '@/portal/network/components/annotation-rows'
import { ServiceSelector } from '@/portal/network/components/service-selector'
import { catalogEntryMatches } from '@/portal/network/ingress-family'
import {
  type NetworkHostOverview,
  type NetworkRoute,
} from '@/portal/network/types'
import { ChevronDown } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
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

interface RouteDialogProps {
  host: NetworkHostOverview
  // null = add a new route; otherwise edit this one.
  editing: NetworkRoute | null
  onClose: () => void
}

// k8s service names: DNS-1123 label, lowercase (the backend re-checks).
const SERVICE_NAME_PATTERN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

const toInput = (route: NetworkRoute) => ({
  path: route.path,
  appName: route.appName,
  port: route.port,
  annotations: route.annotations,
})

const fromRows = (rows: AnnotationRow[]) => {
  const annotations: Record<string, string> = {}
  for (const row of rows) {
    if (row.key === '') continue
    annotations[row.key] = row.value
  }
  return Object.keys(annotations).length > 0 ? annotations : undefined
}

// Card-level route form: the host is fixed context, so the form only
// carries path/service/port. Duplicate paths are checked against the host's
// other routes — the edited route itself is excluded. Saving submits the
// host's full next route table (PUT replace semantics). The service picker
// lists the cluster's live services in the host's space and still accepts a
// manual name (the service may not exist yet); a listed service turns the
// port field into a picker over its declared ports. A collapsible section
// edits service-level annotations from the platform catalog.
export function RouteDialog({ host, editing, onClose }: RouteDialogProps) {
  const { t } = useTranslation()
  const replaceRoutesMutation = useReplaceRoutesMutation()
  const [path, setPath] = useState(editing?.path ?? '/')
  const [appName, setAppName] = useState(editing?.appName ?? '')
  const [port, setPort] = useState(editing ? String(editing.port) : '')
  const [annotationRows, setAnnotationRows] = useState<AnnotationRow[]>(() =>
    Object.entries(editing?.annotations ?? {}).map(([key, value]) => ({
      key,
      value,
    }))
  )
  const [annotationsOpen, setAnnotationsOpen] = useState(false)

  // Live services of the host's cluster+space. placeholderData keeps the
  // previous list mounted across refetches, so it is not trusted for port
  // options: only a service present in non-placeholder data counts.
  const servicesQuery = useNetworkServices(host.cluster, host.space)
  const services = servicesQuery.isPlaceholderData
    ? []
    : (servicesQuery.data ?? [])
  const pickedService = services.find((service) => service.name === appName)

  const suggestedPorts = useMemo(() => {
    if (!pickedService) return []
    const ports = pickedService.ports
      .map((entry) => entry.port)
      .filter((candidate) => Number.isInteger(candidate) && candidate > 0)
    return [...new Set(ports)].sort((a, b) => a - b)
  }, [pickedService])

  // A listed service turns the port field into a picker over its declared
  // ports; snap to the first one when the current value is not among them
  // (stale route port or leftovers from a manual service name).
  useEffect(() => {
    if (suggestedPorts.length > 0 && !suggestedPorts.includes(Number(port))) {
      setPort(String(suggestedPorts[0]))
    }
  }, [port, suggestedPorts])

  // Annotation catalog filtered by the host's class or its controller
  // family, mirroring the host-level annotation dialog.
  const { data: ingressClasses } = useNetworkIngressClasses(host.cluster)
  const { data: catalogData } = useAnnotationKeys()
  const catalog = useMemo(
    () =>
      (catalogData ?? []).filter((entry) =>
        catalogEntryMatches(
          entry.ingressClass,
          host.ingressClass,
          ingressClasses ?? []
        )
      ),
    [catalogData, host.ingressClass, ingressClasses]
  )

  const normalizedPath = path.trim() || '/'
  const portNumber = Number(port)
  const appNameValid =
    appName.trim() !== '' &&
    appName.trim().length <= 63 &&
    SERVICE_NAME_PATTERN.test(appName.trim())
  const valid =
    appNameValid &&
    port !== '' &&
    Number.isInteger(portNumber) &&
    portNumber >= 1 &&
    portNumber <= 65535
  const pathTaken = host.routes.some(
    (route) => route.path === normalizedPath && route.path !== editing?.path
  )

  const save = () => {
    if (!valid || pathTaken) return
    const input = {
      path: normalizedPath,
      appName: appName.trim(),
      port: portNumber,
      annotations: fromRows(annotationRows),
    }
    // Annotations are service-level: the edited set applies to every route
    // of the same service (clearing here clears them all), matching the
    // backend's per-appName normalization.
    const withServiceAnnotations = (route: NetworkRoute) => {
      const base = toInput(route)
      return route.appName === input.appName
        ? { ...base, annotations: input.annotations }
        : base
    }
    // appNamespace is derived from the host's space server-side, so it stays
    // out of the submitted table.
    const nextRoutes = editing
      ? host.routes.map((route) =>
          route.path === editing.path ? input : withServiceAnnotations(route)
        )
      : [...host.routes.map(withServiceAnnotations), input]
    replaceRoutesMutation.mutate(
      {
        space: host.space,
        cluster: host.cluster,
        host: host.host,
        routes: nextRoutes,
      },
      {
        onSuccess: () => {
          toast.success(
            t('network.toast.routeSaved', { host: host.host, path: input.path })
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
      {/* grid-cols-1 pins the content track to minmax(0,1fr): the auto track
          would size to the annotation row's nowrap key text and overflow. */}
      <DialogContent className="grid-cols-1 sm:max-w-md">
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
            <Label>{t('network.routeDialog.service')}</Label>
            <ServiceSelector
              value={appName}
              onChange={setAppName}
              options={services.map((service) => ({ name: service.name }))}
              loading={servicesQuery.isLoading}
              placeholder={t('network.routeDialog.servicePlaceholder')}
              searchPlaceholder={t('network.routeDialog.servicePickerSearch')}
              emptyText={t('network.routeDialog.servicePickerEmpty')}
              customText={t('network.routeDialog.servicePickerCustom')}
            />
            {appName.trim() !== '' && !appNameValid && (
              <p className="text-xs text-destructive">
                {t('network.routeDialog.serviceNameInvalid')}
              </p>
            )}
          </div>
          <div className="space-y-1.5">
            <Label>{t('network.routeDialog.port')}</Label>
            {suggestedPorts.length > 0 ? (
              <Select value={port} onValueChange={setPort}>
                <SelectTrigger className="w-full">
                  <SelectValue
                    placeholder={t('network.routeDialog.portPlaceholder')}
                  />
                </SelectTrigger>
                <SelectContent>
                  {suggestedPorts.map((candidate) => (
                    <SelectItem key={candidate} value={String(candidate)}>
                      {candidate}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : (
              <Input
                type="number"
                value={port}
                onChange={(event) => setPort(event.target.value)}
                placeholder={t('network.routeDialog.portPlaceholder')}
              />
            )}
          </div>
          <Collapsible open={annotationsOpen} onOpenChange={setAnnotationsOpen}>
            <CollapsibleTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 px-2 text-xs text-muted-foreground"
              >
                <ChevronDown
                  className={cn(
                    'h-3.5 w-3.5 transition-transform',
                    annotationsOpen && 'rotate-180'
                  )}
                />
                {t('network.routeDialog.serviceAnnotations')}
              </Button>
            </CollapsibleTrigger>
            <CollapsibleContent className="pt-2">
              <AnnotationRows
                rows={annotationRows}
                onChange={setAnnotationRows}
                catalog={catalog}
              />
            </CollapsibleContent>
          </Collapsible>
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
          <Button
            disabled={!valid || pathTaken || replaceRoutesMutation.isPending}
            onClick={save}
          >
            {t('network.routeDialog.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
