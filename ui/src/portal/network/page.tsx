import { useMemo, useState } from 'react'
import {
  useNetworkHosts,
  useReplaceAnnotationsMutation,
  useReplaceRoutesMutation,
} from '@/portal/network/api'
import { AnnotationsDialog } from '@/portal/network/components/annotations-dialog'
import { NetworkHostCard } from '@/portal/network/components/host-card'
import { HostCreateDialog } from '@/portal/network/components/host-create-dialog'
import { HostSettingsDialog } from '@/portal/network/components/host-settings-dialog'
import { NetworkImportDialog } from '@/portal/network/components/import-dialog'
import { RouteDialog } from '@/portal/network/components/route-dialog'
import {
  type NetworkHostOverview,
  type NetworkRoute,
} from '@/portal/network/types'
import { useSpaces } from '@/portal/space-context'
import { FileUp, Plus, Search } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { useCurrentClusterList } from '@/lib/api/cluster'
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

interface RouteTarget {
  cluster: string
  hostName: string
  editing: NetworkRoute | null
}

export function NetworkPage() {
  const { t } = useTranslation()
  const { currentSpace } = useSpaces()
  usePageTitle(t('network.title'))
  const [search, setSearch] = useState('')
  const [selectedCluster, setSelectedCluster] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [annotationTarget, setAnnotationTarget] =
    useState<NetworkHostOverview | null>(null)
  const [hostSettingsTarget, setHostSettingsTarget] =
    useState<NetworkHostOverview | null>(null)
  const [routeTarget, setRouteTarget] = useState<RouteTarget | null>(null)

  // Hosts are registered per space; the cluster picker offers the platform's
  // real (RBAC-filtered) cluster list.
  const { data: clusterList } = useCurrentClusterList()
  const clusterNames = useMemo(
    () => (clusterList ?? []).map((cluster) => cluster.name).sort(),
    [clusterList]
  )

  // Single-select view scope; falls back to the first cluster when the
  // selection disappears (e.g. the cluster was removed from the platform).
  const activeCluster =
    clusterNames.find((name) => name === selectedCluster) ?? clusterNames[0]

  const hostsQuery = useNetworkHosts(currentSpace ?? undefined, activeCluster)
  const hosts = useMemo(() => hostsQuery.data ?? [], [hostsQuery.data])

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

  // Route writes are full-table replacements (H4 semantics): deleting one
  // route submits the host's remaining table.
  const replaceRoutesMutation = useReplaceRoutesMutation()
  const replaceAnnotationsMutation = useReplaceAnnotationsMutation()

  const saveAnnotations = (
    hostName: string,
    annotations: Record<string, string>
  ) => {
    const target = annotationTarget
    if (!target) return
    replaceAnnotationsMutation.mutate(
      {
        space: target.space,
        cluster: target.cluster,
        host: hostName,
        annotations,
      },
      {
        onSuccess: () =>
          toast.success(
            t('network.toast.annotationsSaved', { host: hostName })
          ),
        onError: (err) => toast.error(err.message),
      }
    )
  }

  const deleteRoute = (host: NetworkHostOverview, path: string) => {
    // The host itself stays: it owns its registration and settings, and an
    // empty route table is a valid intermediate state. Untouched routes keep
    // their service annotations.
    replaceRoutesMutation.mutate(
      {
        space: host.space,
        cluster: host.cluster,
        host: host.host,
        routes: host.routes
          .filter((route) => route.path !== path)
          .map(({ path, appName, port, annotations }) => ({
            path,
            appName,
            port,
            annotations,
          })),
      },
      {
        onSuccess: () =>
          toast.success(
            t('network.toast.routeDeleted', { host: host.host, path })
          ),
        onError: (err) => toast.error(err.message),
      }
    )
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
            <Button variant="outline" onClick={() => setImportOpen(true)}>
              <FileUp className="h-3.5 w-3.5" />
              {t('network.import.action')}
            </Button>
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

      {!currentSpace ? (
        <div className="py-12 text-center text-sm text-muted-foreground">
          {t('network.needSpace')}
        </div>
      ) : hostsQuery.isLoading ? (
        <div className="py-12 text-center text-sm text-muted-foreground">
          {t('network.loading')}
        </div>
      ) : hostsQuery.isError ? (
        <div className="flex flex-col items-center gap-1 py-12 text-center">
          <p className="text-sm font-medium text-muted-foreground">
            {t('network.loadFailed')}
          </p>
          <p className="text-xs text-muted-foreground">
            {hostsQuery.error.message}
          </p>
        </div>
      ) : filtered.length === 0 ? (
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
              onDeleteRoute={deleteRoute}
              onAddRoute={(hostName) =>
                setRouteTarget({
                  cluster: host.cluster,
                  hostName,
                  editing: null,
                })
              }
              onEditRoute={(hostName, route) =>
                setRouteTarget({
                  cluster: host.cluster,
                  hostName,
                  editing: route,
                })
              }
              onEditHost={setHostSettingsTarget}
              onEditAnnotations={setAnnotationTarget}
            />
          ))}
        </div>
      )}

      {createOpen && activeCluster && currentSpace && (
        <HostCreateDialog
          space={currentSpace}
          cluster={activeCluster}
          hosts={hosts}
          onClose={() => setCreateOpen(false)}
        />
      )}

      {importOpen && activeCluster && currentSpace && (
        <NetworkImportDialog
          space={currentSpace}
          clusterNames={clusterNames}
          defaultCluster={activeCluster}
          onClose={() => setImportOpen(false)}
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
        />
      )}

      {routeTarget && routeDialogHost && (
        <RouteDialog
          key={`${routeTarget.hostName}:${routeTarget.editing?.path ?? '+'}`}
          host={routeDialogHost}
          editing={routeTarget.editing}
          onClose={() => setRouteTarget(null)}
        />
      )}
    </div>
  )
}
