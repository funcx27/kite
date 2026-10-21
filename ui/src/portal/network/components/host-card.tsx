import { ActionTable } from '@/portal/components/action-table'
import {
  type NetworkConfigStatus,
  type NetworkHostOverview,
  type NetworkRoute,
} from '@/portal/network/types'
import { ColumnDef } from '@tanstack/react-table'
import {
  ExternalLink,
  Lock,
  LockOpen,
  MoreHorizontal,
  Plus,
  RefreshCw,
  Tag,
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

// Local to this module on purpose: the network page is a frontend mock and
// must not reach into the application module's status styling.
const configDotStyles: Record<NetworkConfigStatus, string> = {
  synced: 'bg-emerald-500',
  drift: 'bg-amber-500',
  pending: 'bg-sky-500',
  error: 'bg-red-500',
  undeployed: 'bg-zinc-400',
}

const EXPIRING_SOON_MS = 30 * 24 * 60 * 60 * 1000

function isExpiringSoon(expiresAt: string): boolean {
  return new Date(expiresAt).getTime() - Date.now() < EXPIRING_SOON_MS
}

interface NetworkHostCardProps {
  host: NetworkHostOverview
  onPublish: (host: string) => void
  onSyncCert: (host: string) => void
  onDeleteRoute: (host: string, path: string) => void
  onAddRoute: (host: string) => void
  onEditRoute: (host: string, route: NetworkRoute) => void
  onEditHost: (host: string) => void
  onEditAnnotations: (host: string) => void
}

export function NetworkHostCard({
  host,
  onPublish,
  onSyncCert,
  onDeleteRoute,
  onAddRoute,
  onEditRoute,
  onEditHost,
  onEditAnnotations,
}: NetworkHostCardProps) {
  const { t } = useTranslation()
  const cert = host.tlsCertificate
  const certPendingSync = !host.certSynced
  // Publishing a route-less host is meaningless; it waits for its first route.
  const needsPublish = host.status !== 'synced' && host.routes.length > 0
  const annotationEntries = Object.entries(host.annotations ?? {})

  const columns: ColumnDef<NetworkRoute>[] = [
    {
      accessorKey: 'path',
      header: t('network.columns.path'),
      cell: ({ row }) => (
        <span className="font-mono text-xs">{row.original.path}</span>
      ),
    },
    {
      accessorKey: 'appName',
      header: t('network.columns.app'),
      cell: ({ row }) => (
        <Link
          to={`/applications/${row.original.appNamespace}/${row.original.appName}`}
          className="text-primary hover:underline"
        >
          {row.original.appName}
        </Link>
      ),
    },
    {
      accessorKey: 'port',
      header: t('network.columns.port'),
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground">
          {row.original.port}
        </span>
      ),
    },
  ]

  return (
    <Card>
      <CardContent className="space-y-3">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="space-y-2">
            <div className="flex flex-wrap items-center gap-2">
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
              <Badge variant="outline">{host.space}</Badge>
              <Badge variant="outline">{host.ingressClass}</Badge>
            </div>
            {cert ? (
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <Lock className="h-3.5 w-3.5 text-emerald-500" />
                <span className="font-mono">{cert.name}</span>
                <span className="text-muted-foreground">
                  {t('network.cert.expires', { date: cert.expiresAt })}
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
                      onClick={() => onSyncCert(host.host)}
                      title={t('network.cert.sync')}
                      className="cursor-pointer border-amber-500/50 text-amber-600 hover:bg-amber-500/10 dark:text-amber-400"
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
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <span className="flex items-center gap-1.5 whitespace-nowrap">
                <span
                  className={cn(
                    'h-2 w-2 shrink-0 rounded-full',
                    configDotStyles[host.status]
                  )}
                />
                <span className="font-mono">{host.cluster}</span>
                <span>{t(`network.status.${host.status}`)}</span>
              </span>
              {host.lbAddress && (
                <span className="text-muted-foreground">
                  · {t('network.lb')} {host.lbAddress}
                </span>
              )}
            </div>
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
                onClick={() => onPublish(host.host)}
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
                <DropdownMenuItem onClick={() => onEditHost(host.host)}>
                  {t('network.actions.editHost')}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => onEditAnnotations(host.host)}>
                  {t('network.actions.editAnnotations')}
                </DropdownMenuItem>
                <DropdownMenuItem
                  onClick={() => toast.info(t('network.toast.mockOnly'))}
                >
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
            actions={[
              {
                label: t('network.actions.edit'),
                onClick: (route) => onEditRoute(host.host, route),
              },
              {
                label: t('network.actions.delete'),
                onClick: (route) => onDeleteRoute(host.host, route.path),
              },
            ]}
          />
        )}
      </CardContent>
    </Card>
  )
}
