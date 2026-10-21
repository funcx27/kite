import { useMemo, useState } from 'react'
import { AnnotationsDialog } from '@/portal/network/components/annotations-dialog'
import { NetworkHostCard } from '@/portal/network/components/host-card'
import { HostCreateDialog } from '@/portal/network/components/host-create-dialog'
import { HostSettingsDialog } from '@/portal/network/components/host-settings-dialog'
import { RouteDialog } from '@/portal/network/components/route-dialog'
import { mockNetworkHosts } from '@/portal/network/mock'
import {
  type NetworkCertificate,
  type NetworkHostOverview,
  type NetworkRoute,
} from '@/portal/network/types'
import { Plus, Search } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { usePageTitle } from '@/hooks/use-page-title'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

// Summary chip: bold count plus its label.
function SummaryChip({ value, label }: { value: number; label: string }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-xs">
      <span className="font-semibold">{value}</span>
      <span className="text-muted-foreground">{label}</span>
    </span>
  )
}

// Host edits rewrite the rendered Ingress; the host goes back to pending
// until republished (undeployed keeps waiting for its first publish).
function markPending(host: NetworkHostOverview): NetworkHostOverview {
  return host.status === 'undeployed'
    ? host
    : { ...host, status: 'pending' as const }
}

interface RouteTarget {
  cluster: string
  hostName: string
  editing: NetworkRoute | null
}

export function NetworkPage() {
  const { t } = useTranslation()
  usePageTitle(t('network.title'))
  // Frontend mock: every action mutates local state only. The real page
  // filters by the current space; the mock shows all hosts and marks each
  // with its space badge instead.
  const [hosts, setHosts] = useState<NetworkHostOverview[]>(mockNetworkHosts)
  const [search, setSearch] = useState('')
  const [selectedCluster, setSelectedCluster] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [annotationTarget, setAnnotationTarget] =
    useState<NetworkHostOverview | null>(null)
  const [hostSettingsTarget, setHostSettingsTarget] =
    useState<NetworkHostOverview | null>(null)
  const [routeTarget, setRouteTarget] = useState<RouteTarget | null>(null)

  const clusterNames = useMemo(
    () => Array.from(new Set(hosts.map((host) => host.cluster))).sort(),
    [hosts]
  )

  // Single-select view scope; falls back to the first cluster when the
  // selection disappears (e.g. its last host was deleted).
  const activeCluster =
    clusterNames.find((name) => name === selectedCluster) ?? clusterNames[0]

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return hosts.filter((host) => {
      if (host.cluster !== activeCluster) return false
      if (
        q &&
        !host.host.toLowerCase().includes(q) &&
        !host.routes.some((route) => route.appName.toLowerCase().includes(q))
      ) {
        return false
      }
      return true
    })
  }, [hosts, activeCluster, search])

  const totalRoutes = filtered.reduce(
    (sum, host) => sum + host.routes.length,
    0
  )
  const pendingConfigs = filtered.filter(
    (host) => host.status === 'pending'
  ).length
  const certPendingConfigs = filtered.filter((host) => !host.certSynced).length

  // Handlers match by name within the active cluster: only that cluster's
  // cards are rendered, and the same name on another cluster is a different
  // registration.
  const inCluster = (host: NetworkHostOverview, hostName: string) =>
    host.host === hostName && host.cluster === activeCluster

  const publish = (hostName: string) => {
    setHosts((prev) =>
      prev.map((host) =>
        inCluster(host, hostName)
          ? { ...host, status: 'synced' as const }
          : host
      )
    )
    toast.success(t('network.toast.published', { host: hostName }))
  }

  const syncCert = (hostName: string) => {
    setHosts((prev) =>
      prev.map((host) =>
        inCluster(host, hostName) ? { ...host, certSynced: true } : host
      )
    )
    toast.success(t('network.toast.certSynced', { host: hostName }))
  }

  const saveAnnotations = (
    hostName: string,
    annotations: Record<string, string>
  ) => {
    setHosts((prev) =>
      prev.map((host) =>
        inCluster(host, hostName) ? { ...markPending(host), annotations } : host
      )
    )
    toast.success(t('network.toast.annotationsSaved', { host: hostName }))
  }

  const saveHostSettings = (
    hostName: string,
    changes: {
      ingressClass: string
      tlsCertificate: NetworkCertificate | null
    }
  ) => {
    setHosts((prev) =>
      prev.map((host) => {
        if (!inCluster(host, hostName)) return host
        const classChanged = changes.ingressClass !== host.ingressClass
        const certChanged =
          (changes.tlsCertificate?.name ?? '') !==
          (host.tlsCertificate?.name ?? '')
        return {
          ...host,
          ingressClass: changes.ingressClass,
          tlsCertificate: changes.tlsCertificate ?? undefined,
          status:
            classChanged && host.status !== 'undeployed'
              ? ('pending' as const)
              : host.status,
          certSynced: certChanged ? false : host.certSynced,
        }
      })
    )
    toast.success(t('network.toast.hostSettingsSaved', { host: hostName }))
  }

  const createHost = (input: {
    host: string
    ingressClass: string
    tlsCertificate: NetworkCertificate | null
  }) => {
    setHosts((prev) => [
      ...prev,
      {
        host: input.host,
        space: 'team-alpha',
        cluster: activeCluster,
        ingressClass: input.ingressClass,
        status: 'undeployed',
        certSynced: !input.tlsCertificate,
        tlsCertificate: input.tlsCertificate ?? undefined,
        routes: [],
      },
    ])
    toast.success(t('network.toast.hostCreated', { host: input.host }))
  }

  const saveRoute = (
    target: RouteTarget,
    input: { path: string; appName: string; port: number }
  ) => {
    const originalPath = target.editing?.path ?? null
    setHosts((prev) =>
      prev.map((host) => {
        if (host.host !== target.hostName || host.cluster !== target.cluster)
          return host
        const routes = originalPath
          ? host.routes.map((route) =>
              route.path === originalPath
                ? {
                    ...route,
                    path: input.path,
                    appName: input.appName,
                    port: input.port,
                  }
                : route
            )
          : [
              ...host.routes,
              {
                path: input.path,
                appName: input.appName,
                appNamespace: host.space,
                port: input.port,
              },
            ]
        return { ...markPending(host), routes }
      })
    )
    toast.success(
      t('network.toast.routeSaved', { host: target.hostName, path: input.path })
    )
  }

  const deleteRoute = (hostName: string, path: string) => {
    // The host itself stays: it owns its registration and settings, and an
    // empty route table is a valid intermediate state.
    setHosts((prev) =>
      prev.map((host) =>
        inCluster(host, hostName)
          ? {
              ...markPending(host),
              routes: host.routes.filter((route) => route.path !== path),
            }
          : host
      )
    )
    toast.success(t('network.toast.routeDeleted', { host: hostName, path }))
  }

  const routeDialogHost = routeTarget
    ? hosts.find(
        (host) =>
          host.host === routeTarget.hostName &&
          host.cluster === routeTarget.cluster
      )
    : undefined

  return (
    <div className="flex flex-col gap-4">
      <div className="space-y-1.5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 className="text-2xl font-bold">{t('network.title')}</h1>
            <p className="text-sm text-muted-foreground">
              {t('network.description')}
            </p>
          </div>
          <div className="flex items-center gap-2">
            {clusterNames.length > 0 && (
              <Select value={activeCluster} onValueChange={setSelectedCluster}>
                <SelectTrigger
                  className="h-9 w-44 font-mono"
                  aria-label={t('network.hostCreate.cluster')}
                >
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
            )}
            <div className="relative">
              <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder={t('network.searchPlaceholder')}
                aria-label={t('network.searchPlaceholder')}
                className="h-9 w-56 pl-9"
              />
            </div>
            <Button onClick={() => setCreateOpen(true)}>
              <Plus className="h-3.5 w-3.5" />
              {t('network.newHost')}
            </Button>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <SummaryChip
            value={filtered.length}
            label={t('network.summary.hosts')}
          />
          <SummaryChip
            value={totalRoutes}
            label={t('network.summary.routes')}
          />
          <SummaryChip
            value={pendingConfigs}
            label={t('network.summary.pending')}
          />
          <SummaryChip
            value={certPendingConfigs}
            label={t('network.summary.certPending')}
          />
        </div>
      </div>

      {filtered.length === 0 ? (
        <div className="flex flex-col items-center gap-1 py-12 text-center">
          <p className="text-sm font-medium text-muted-foreground">
            {t('network.emptyTitle')}
          </p>
          <p className="text-sm text-muted-foreground">
            {t('network.emptyDescription')}
          </p>
        </div>
      ) : (
        <div className="flex flex-col gap-4">
          {filtered.map((host) => (
            <NetworkHostCard
              key={`${host.cluster}/${host.host}`}
              host={host}
              onPublish={publish}
              onSyncCert={syncCert}
              onDeleteRoute={deleteRoute}
              onAddRoute={(hostName) =>
                setRouteTarget({
                  cluster: activeCluster ?? '',
                  hostName,
                  editing: null,
                })
              }
              onEditRoute={(hostName, route) =>
                setRouteTarget({
                  cluster: activeCluster ?? '',
                  hostName,
                  editing: route,
                })
              }
              onEditHost={(hostName) =>
                setHostSettingsTarget(
                  hosts.find((host) => inCluster(host, hostName)) ?? null
                )
              }
              onEditAnnotations={(hostName) =>
                setAnnotationTarget(
                  hosts.find((host) => inCluster(host, hostName)) ?? null
                )
              }
            />
          ))}
        </div>
      )}

      {createOpen && activeCluster && (
        <HostCreateDialog
          hosts={hosts}
          cluster={activeCluster}
          onClose={() => setCreateOpen(false)}
          onCreate={createHost}
        />
      )}

      {/* Keyed by host so reopening always starts from the saved state. */}
      {annotationTarget && (
        <AnnotationsDialog
          key={annotationTarget.host}
          host={annotationTarget}
          onClose={() => setAnnotationTarget(null)}
          onSave={saveAnnotations}
        />
      )}

      {hostSettingsTarget && (
        <HostSettingsDialog
          key={hostSettingsTarget.host}
          host={hostSettingsTarget}
          onClose={() => setHostSettingsTarget(null)}
          onSave={saveHostSettings}
        />
      )}

      {routeTarget && routeDialogHost && (
        <RouteDialog
          key={`${routeTarget.hostName}:${routeTarget.editing?.path ?? '+'}`}
          host={routeDialogHost}
          editing={routeTarget.editing}
          onClose={() => setRouteTarget(null)}
          onSave={(input) => saveRoute(routeTarget, input)}
        />
      )}
    </div>
  )
}
