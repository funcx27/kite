import { useState, type ComponentType } from 'react'
import { useApplications } from '@/portal/application/api'
import {
  AlbIcon,
  GlobeIcon,
  KongIcon,
  NginxIcon,
  TraefikIcon,
} from '@/portal/application/components/brand-icons'
import { ActionTable } from '@/portal/components/action-table'
import { downloadTextFile } from '@/portal/components/download'
import {
  fetchHostExport,
  useNetworkIngressClasses,
  usePublishHostMutation,
  useSyncCertMutation,
} from '@/portal/network/api'
import { OfflineDialog } from '@/portal/network/components/offline-dialog'
import {
  familyOfController,
  type IngressFamily,
} from '@/portal/network/ingress-family'
import {
  type NetworkConfigStatus,
  type NetworkHostOverview,
  type NetworkRoute,
  type NetworkRouteIssue,
} from '@/portal/network/types'
import { ColumnDef } from '@tanstack/react-table'
import { format } from 'date-fns'
import {
  ExternalLink,
  Lock,
  LockOpen,
  MoreHorizontal,
  Pencil,
  Plus,
  RefreshCw,
  Tag,
  Trash2,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'

import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

// Local to this module on purpose: the application module's status styling
// tracks workload state, not Ingress desired-vs-live state. Drives both the
// card's left color bar and the status dot next to the host name.
const configDotStyles: Record<NetworkConfigStatus, string> = {
  synced: 'bg-emerald-500',
  drift: 'bg-amber-500',
  pending: 'bg-sky-500',
  error: 'bg-red-500',
  undeployed: 'bg-zinc-400',
}

const familyIcons: Record<
  IngressFamily,
  ComponentType<{ className?: string }>
> = {
  nginx: NginxIcon,
  traefik: TraefikIcon,
  kong: KongIcon,
  alb: AlbIcon,
  other: GlobeIcon,
}

const EXPIRING_SOON_MS = 30 * 24 * 60 * 60 * 1000

function isExpiringSoon(expiresAt: string): boolean {
  return new Date(expiresAt).getTime() - Date.now() < EXPIRING_SOON_MS
}

// One red dot per cell carrying the route's server-side issues; the
// tooltip spells out each problem (missing service/port, path drift).
function IssueDot({ issues }: { issues: NetworkRouteIssue[] | undefined }) {
  const { t } = useTranslation()
  if (!issues || issues.length === 0) return null
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="h-1.5 w-1.5 shrink-0 cursor-default rounded-full bg-red-500" />
      </TooltipTrigger>
      <TooltipContent className="flex max-w-60 flex-col gap-0.5 whitespace-normal">
        {issues.map((issue) => (
          <span key={issue}>{t(`network.issues.${issue}`)}</span>
        ))}
      </TooltipContent>
    </Tooltip>
  )
}

interface NetworkHostCardProps {
  host: NetworkHostOverview
  onDeleteRoute: (host: NetworkHostOverview, path: string) => void
  onAddRoute: (host: string) => void
  onEditRoute: (host: string, route: NetworkRoute) => void
  onEditHost: (host: NetworkHostOverview) => void
  onEditAnnotations: (host: NetworkHostOverview) => void
}

export function NetworkHostCard({
  host,
  onDeleteRoute,
  onAddRoute,
  onEditRoute,
  onEditHost,
  onEditAnnotations,
}: NetworkHostCardProps) {
  const { t } = useTranslation()
  const publishMutation = usePublishHostMutation()
  const syncCertMutation = useSyncCertMutation()
  const [offlineOpen, setOfflineOpen] = useState(false)
  const [exporting, setExporting] = useState(false)

  const { data: ingressClasses } = useNetworkIngressClasses(host.cluster)
  const family = familyOfController(
    ingressClasses?.find((entry) => entry.name === host.ingressClass)
      ?.controller ?? ''
  )
  const FamilyIcon = familyIcons[family]

  const cert = host.tlsCertificate
  const certDomains = cert?.domains ?? []
  const certPendingSync = !host.certSynced
  // Publishing a route-less host is meaningless; it waits for its first route.
  const needsPublish = host.status !== 'synced' && host.routes.length > 0
  const annotationEntries = Object.entries(host.annotations ?? {})
  const failureReason =
    host.status === 'error' && host.lastError !== '' ? host.lastError : null

  // Routes link to the target only when it is a kite application in the
  // host's space; plain service names stay text.
  const { data: apps } = useApplications()
  const isKiteApp = (appName: string) =>
    apps?.some((app) => app.namespace === host.space && app.name === appName)

  const hostIdentity = {
    space: host.space,
    cluster: host.cluster,
    host: host.host,
  }

  const publish = () => {
    publishMutation.mutate(hostIdentity, {
      onSuccess: () =>
        toast.success(t('network.toast.published', { host: host.host })),
      onError: (err) => toast.error(err.message),
    })
  }

  const syncCert = () => {
    syncCertMutation.mutate(hostIdentity, {
      onSuccess: () =>
        toast.success(t('network.toast.certSynced', { host: host.host })),
      onError: (err) => toast.error(err.message),
    })
  }

  const exportYaml = async () => {
    setExporting(true)
    try {
      const text = await fetchHostExport(host.space, host.cluster, host.host)
      downloadTextFile(`${host.host}.yaml`, text, 'text/yaml')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : String(err))
    } finally {
      setExporting(false)
    }
  }

  const columns: ColumnDef<NetworkRoute>[] = [
    {
      accessorKey: 'path',
      header: t('network.columns.path'),
      cell: ({ row }) => (
        <span className="flex items-center gap-1.5 font-mono text-xs">
          {/* Each dot marks its own column: path drift on the path, service
              problems on the service. */}
          <IssueDot
            issues={row.original.issues?.filter(
              (issue) => issue === 'path-drift'
            )}
          />
          {row.original.path}
        </span>
      ),
    },
    {
      accessorKey: 'appName',
      header: t('network.columns.service'),
      cell: ({ row }) => {
        const service = `${row.original.appName}:${row.original.port}`
        return (
          <span className="flex items-center gap-1.5 text-xs">
            <IssueDot
              issues={row.original.issues?.filter(
                (issue) => issue !== 'path-drift'
              )}
            />
            {isKiteApp(row.original.appName) ? (
              <Link
                to={`/applications/${host.space}/${row.original.appName}`}
                className="font-mono text-primary hover:underline"
              >
                {service}
              </Link>
            ) : (
              <span className="font-mono">{service}</span>
            )}
          </span>
        )
      },
    },
  ]

  return (
    <Card className="relative overflow-hidden">
      <span
        className={cn(
          'absolute inset-y-0 left-0 w-1',
          configDotStyles[host.status]
        )}
      />
      <CardContent className="space-y-3 pl-4">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <Tooltip>
                <TooltipTrigger asChild>
                  <span
                    className={cn(
                      'h-2 w-2 shrink-0 cursor-default rounded-full',
                      configDotStyles[host.status]
                    )}
                  />
                </TooltipTrigger>
                <TooltipContent className="flex max-w-60 flex-col gap-0.5 whitespace-normal">
                  <span>{t(`network.status.${host.status}`)}</span>
                  {failureReason && <span>{host.lastError}</span>}
                  {host.lbAddress && (
                    <span>
                      {t('network.lb')} {host.lbAddress}
                    </span>
                  )}
                </TooltipContent>
              </Tooltip>
              <span className="font-mono font-semibold">{host.host}</span>
              <a
                href={`${cert ? 'https' : 'http'}://${host.host}`}
                target="_blank"
                rel="noreferrer"
                aria-label={host.host}
                className="text-muted-foreground hover:text-foreground"
              >
                <ExternalLink className="h-3.5 w-3.5" />
              </a>
              <Tooltip>
                <TooltipTrigger asChild>
                  <span className="cursor-default">
                    <FamilyIcon className="h-4 w-4" />
                  </span>
                </TooltipTrigger>
                <TooltipContent>{host.ingressClass}</TooltipContent>
              </Tooltip>
            </div>
            {cert ? (
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <Lock className="h-3.5 w-3.5 text-emerald-500" />
                <Tooltip>
                  <TooltipTrigger asChild>
                    <span className="cursor-default font-mono">
                      {certDomains[0] ?? cert.name}
                    </span>
                  </TooltipTrigger>
                  <TooltipContent className="flex max-w-60 flex-col gap-0.5 whitespace-normal">
                    {certDomains.map((domain) => (
                      <span key={domain} className="font-mono">
                        {domain}
                      </span>
                    ))}
                    <span>
                      {t('network.cert.expires', {
                        date: format(new Date(cert.expiresAt), 'yyyy-MM-dd'),
                      })}
                    </span>
                  </TooltipContent>
                </Tooltip>
                <span className="text-muted-foreground">
                  {t('network.cert.expires', {
                    date: format(new Date(cert.expiresAt), 'yyyy-MM-dd'),
                  })}
                </span>
                {isExpiringSoon(cert.expiresAt) && (
                  <Badge
                    variant="outline"
                    className="border-amber-500/50 text-amber-600 dark:text-amber-400"
                  >
                    {t('network.cert.expiringSoon')}
                  </Badge>
                )}
                {certPendingSync && (
                  <Badge variant="outline" asChild>
                    <button
                      type="button"
                      disabled={syncCertMutation.isPending}
                      onClick={syncCert}
                      title={t('network.cert.sync')}
                      className="cursor-pointer border-amber-500/50 text-amber-600 hover:bg-amber-500/10 disabled:cursor-not-allowed disabled:opacity-60 dark:text-amber-400"
                    >
                      <RefreshCw />
                      {t('network.cert.pendingSync')}
                    </button>
                  </Badge>
                )}
              </div>
            ) : (
              <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                <LockOpen className="h-3.5 w-3.5" />
                {t('network.cert.none')}
              </div>
            )}
            {annotationEntries.length > 0 && (
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
                <Tag className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                {annotationEntries.map(([key, value]) => (
                  <span
                    key={key}
                    title={`${key}: ${value}`}
                    className="font-mono text-muted-foreground"
                  >
                    {key}: {value}
                  </span>
                ))}
              </div>
            )}
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => onAddRoute(host.host)}
            >
              <Plus className="h-3.5 w-3.5" />
              {t('network.actions.addRoute')}
            </Button>
            {needsPublish && (
              <Button
                variant="outline"
                size="sm"
                disabled={publishMutation.isPending}
                onClick={publish}
              >
                <RefreshCw className="h-3.5 w-3.5" />
                {t('network.actions.publish')}
              </Button>
            )}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7"
                  aria-label={host.host}
                >
                  <MoreHorizontal className="h-4 w-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onClick={() => onEditHost(host)}>
                  {t('network.actions.editHost')}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => onEditAnnotations(host)}>
                  {t('network.actions.editAnnotations')}
                </DropdownMenuItem>
                <DropdownMenuItem
                  disabled={exporting}
                  onClick={() => void exportYaml()}
                >
                  {t('network.actions.exportYaml')}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setOfflineOpen(true)}>
                  {t('network.actions.offline')}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>
        {host.routes.length === 0 ? (
          <div className="flex items-center justify-center rounded-md border border-dashed py-6 text-xs text-muted-foreground">
            {t('network.emptyRoutes')}
          </div>
        ) : (
          <ActionTable<NetworkRoute>
            data={host.routes}
            columns={columns}
            actions={[]}
            renderActions={(route) => (
              <>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      aria-label={t('network.actions.edit')}
                      onClick={() => onEditRoute(host.host, route)}
                    >
                      <Pencil className="h-3.5 w-3.5" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>{t('network.actions.edit')}</TooltipContent>
                </Tooltip>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      aria-label={t('network.actions.delete')}
                      onClick={() => onDeleteRoute(host, route.path)}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>{t('network.actions.delete')}</TooltipContent>
                </Tooltip>
              </>
            )}
          />
        )}
      </CardContent>
      {offlineOpen && (
        <OfflineDialog host={host} onClose={() => setOfflineOpen(false)} />
      )}
    </Card>
  )
}
