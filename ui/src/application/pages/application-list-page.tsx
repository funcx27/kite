import { useMemo, useState } from 'react'
import {
  deleteApplication,
  fetchApplicationStatus,
  updateApplication,
  useApplications,
} from '@/application/api'
import { ApplicationWizard } from '@/application/components/application-wizard'
import { DeployDialog } from '@/application/components/deploy-dialog'
import {
  ApplicationConfigStatus,
  StatusDotGlyph,
} from '@/application/components/status-badge'
import {
  clusterHealth,
  clusterHealthLabel,
  type ApplicationDotState,
} from '@/application/lib'
import type { Application } from '@/application/types'
import {
  useSpaces,
  useSyncSpaceParam,
  withSpaceParam,
} from '@/platform/space-context'
import { ColumnDef } from '@tanstack/react-table'
import { Check, ChevronDown, Server } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate } from 'react-router-dom'
import { toast } from 'sonner'

import { cn, formatDate, translateError } from '@/lib/utils'
import { useCluster } from '@/hooks/use-cluster'
import { usePageTitle } from '@/hooks/use-page-title'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { ActionTable } from '@/components/action-table'

// Application list (design §11): ActionTable with a ready-first status column
// (one row per cluster config), a space column, a cluster filter in the
// toolbar (chips for a handful of clusters, a picker popover beyond that),
// and the first-publish entry on undeployed apps.

// Mirrors the backend delete gate (apps_api.go DeleteApp): only configs with
// a consistency anchor — deployed, not offlined (StatusSummary=undeployed) or
// never deployed (empty) — block deletion.
function hasDeployedConfig(app: Application): boolean {
  return app.configs.some(
    (config) =>
      config.statusSummary !== 'undeployed' && config.statusSummary !== ''
  )
}

export function ApplicationListPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { currentSpace, spaceClusters } = useSpaces()
  const { clusters } = useCluster()
  useSyncSpaceParam()
  const { data: applications, isLoading, refetch } = useApplications()
  usePageTitle(t('nav.applications'))

  const [search, setSearch] = useState('')
  const [clusterFilters, setClusterFilters] = useState<string[]>([])
  const [refreshing, setRefreshing] = useState(false)
  const [wizardOpen, setWizardOpen] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<Application | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [describeTarget, setDescribeTarget] = useState<Application | null>(null)
  const [description, setDescription] = useState('')
  const [savingDescription, setSavingDescription] = useState(false)
  // Undeployed app picked for first publish (deploy dialog with cluster
  // selection, design §11).
  const [firstPublishTarget, setFirstPublishTarget] =
    useState<Application | null>(null)
  const [wizardMode, setWizardMode] = useState<'create' | 'draft'>('create')

  // Clusters a config filter can pick: the current space's clusters unioned
  // with the clusters its apps actually have configs on (an unlinked config's
  // cluster stays pickable), or every healthy cluster in the all-spaces view.
  const clusterNames = useMemo(() => {
    if (currentSpace) {
      const names = new Set(spaceClusters[currentSpace] ?? [])
      for (const app of applications ?? []) {
        if (app.namespace !== currentSpace) continue
        for (const config of app.configs) names.add(config.cluster)
      }
      return Array.from(names).sort()
    }
    const names = new Set(
      clusters.filter((cluster) => !cluster.error).map((c) => c.name)
    )
    return Array.from(names).sort()
  }, [currentSpace, spaceClusters, clusters, applications])

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return (applications ?? []).filter((app) => {
      if (currentSpace && app.namespace !== currentSpace) return false
      if (
        q &&
        !app.name.includes(q) &&
        !(app.description ?? '').toLowerCase().includes(q)
      ) {
        return false
      }
      if (
        clusterFilters.length > 0 &&
        // Draft-only apps carry no cluster signal; keep them under any
        // cluster filter instead of vanishing.
        app.configs.length > 0 &&
        !app.configs.some((c) => clusterFilters.includes(c.cluster))
      ) {
        return false
      }
      return true
    })
  }, [applications, currentSpace, search, clusterFilters])

  const toggleClusterFilter = (name: string) =>
    setClusterFilters((prev) =>
      prev.includes(name) ? prev.filter((c) => c !== name) : [...prev, name]
    )

  const refreshStatuses = async () => {
    setRefreshing(true)
    try {
      const checks = filtered.flatMap((app) =>
        app.configs.map((config) =>
          fetchApplicationStatus(app.name, config.cluster).catch(() => null)
        )
      )
      await Promise.all(checks)
      await refetch()
      toast.success(t('applications.list.statusRefreshed'))
    } finally {
      setRefreshing(false)
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget) return
    setDeleting(true)
    try {
      await deleteApplication(deleteTarget.name)
      toast.success(t('applications.list.deleted', { name: deleteTarget.name }))
      setDeleteTarget(null)
      await refetch()
    } catch (err) {
      toast.error(translateError(err, t))
    } finally {
      setDeleting(false)
    }
  }

  const saveDescription = async () => {
    if (!describeTarget) return
    setSavingDescription(true)
    try {
      await updateApplication(describeTarget.name, { description })
      toast.success(t('applications.list.descriptionSaved'))
      setDescribeTarget(null)
      await refetch()
    } catch (err) {
      toast.error(translateError(err, t))
    } finally {
      setSavingDescription(false)
    }
  }

  const columns: ColumnDef<Application>[] = [
    {
      accessorKey: 'name',
      header: () => (
        <span className="flex items-center gap-2">
          {t('applications.list.name')}
          <Input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t('applications.list.search')}
            aria-label={t('applications.list.search')}
            className="h-7 w-36 font-normal"
          />
        </span>
      ),
      cell: ({ row }) => (
        <Link
          to={withSpaceParam(
            `/applications/${row.original.name}`,
            currentSpace
          )}
          className="font-mono font-medium hover:underline"
        >
          {row.original.name}
        </Link>
      ),
    },
    {
      accessorKey: 'namespace',
      header: t('sidebar.spaces.label'),
      cell: ({ row }) => (
        <span className="inline-flex items-center rounded-full border bg-muted/40 px-2 py-0.5 text-xs text-muted-foreground">
          {row.original.namespace}
        </span>
      ),
    },
    {
      accessorKey: 'description',
      header: t('applications.wizard.description'),
      cell: ({ row }) => (
        <span className="text-muted-foreground">
          {row.original.description || '—'}
        </span>
      ),
    },
    {
      id: 'status',
      header: t('applications.list.status'),
      cell: ({ row }) => {
        const configs = row.original.configs
        if (configs.length === 0) {
          return (
            <span
              className="flex items-center gap-1.5 whitespace-nowrap"
              title={t('applications.status.undeployed')}
            >
              <StatusDotGlyph state="undeployed" />
              <span className="text-xs text-muted-foreground">
                {t('applications.status.undeployed')}
              </span>
            </span>
          )
        }
        return (
          <div className="flex flex-col gap-1">
            {configs.map((config) => (
              <ApplicationConfigStatus key={config.id} config={config} />
            ))}
          </div>
        )
      },
    },
    {
      id: 'lastDeployed',
      header: t('applications.list.lastDeployed'),
      cell: ({ row }) => {
        const times = row.original.configs
          .map((config) => config.lastDeployedAt)
          .filter((time): time is string => !!time)
          .sort()
        const latest = times[times.length - 1]
        return (
          <span className="text-xs text-muted-foreground">
            {latest ? formatDate(latest) : '—'}
          </span>
        )
      },
    },
  ]

  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-wrap items-center gap-2">
            {clusterNames.length > 4 ? (
              <ClusterFilterPicker
                clusters={clusterNames}
                selected={clusterFilters}
                onChange={setClusterFilters}
              />
            ) : (
              clusterNames.length > 0 && (
                <div className="flex flex-wrap items-center gap-1.5">
                  {clusterNames.map((name) => {
                    const active = clusterFilters.includes(name)
                    return (
                      <button
                        key={name}
                        type="button"
                        onClick={() => toggleClusterFilter(name)}
                        className={cn(
                          'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 font-mono text-xs transition-colors',
                          active
                            ? 'border-primary bg-primary/10 font-medium text-foreground'
                            : 'text-muted-foreground hover:bg-accent'
                        )}
                      >
                        <ClusterHealthDot cluster={name} />
                        {name}
                      </button>
                    )
                  })}
                </div>
              )
            )}
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              disabled={refreshing || filtered.length === 0}
              onClick={() => void refreshStatuses()}
              title={t('applications.list.refreshStatusHint')}
            >
              {t('applications.list.refreshStatus')}
            </Button>
            <Button
              onClick={() => {
                setWizardMode('create')
                setWizardOpen(true)
              }}
            >
              ＋ {t('applications.list.create')}
            </Button>
          </div>
        </div>
        {/* Cluster-layer legend, right under the selector whose chips/picker
          rows it explains. */}
        <div className="flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
          <span className="font-medium">
            {t('applications.list.legendCluster')}
          </span>
          <LegendItem
            state="synced"
            label={t('applications.status.clusterOk')}
          />
          <LegendItem
            state="clusterUnreachable"
            label={t('applications.status.clusterUnreachable')}
          />
        </div>
      </div>

      <ActionTable<Application>
        data={filtered}
        columns={columns}
        actions={[
          {
            label: t('applications.list.view'),
            onClick: (app) =>
              navigate(
                withSpaceParam(`/applications/${app.name}`, currentSpace)
              ),
          },
          {
            label: t('applications.detail.firstPublish'),
            shouldDisable: (app) => app.configs.length > 0,
            onClick: (app) => setFirstPublishTarget(app),
          },
          {
            label: t('applications.list.editDescription'),
            onClick: (app) => {
              setDescribeTarget(app)
              setDescription(app.description ?? '')
            },
          },
          {
            label: t('common.actions.delete'),
            shouldDisable: hasDeployedConfig,
            onClick: (app) => setDeleteTarget(app),
          },
        ]}
      />

      {/* App-layer legend for the ready-first status column; the cluster
          row lives under the cluster selector. Drift and space-unlink are
          detail-page concerns and stay out of the overview. */}
      <div className="flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
        <span className="font-medium">{t('applications.list.legendApp')}</span>
        <LegendItem state="synced" label={t('applications.list.ready')} />
        <LegendItem state="error" label={t('applications.list.notReady')} />
        <LegendItem state="error" label={t('applications.status.error')} />
        <LegendItem
          state="undeployed"
          label={t('applications.status.undeployed')}
        />
        <LegendItem
          state="undeployed"
          label={t('applications.list.unchecked')}
        />
        <span className="ml-auto">
          {t('applications.list.aggregationHint')}
        </span>
      </div>

      <ApplicationWizard
        open={wizardOpen}
        onOpenChange={setWizardOpen}
        mode={wizardMode}
        app={
          wizardMode === 'draft' ? (firstPublishTarget ?? undefined) : undefined
        }
        onSaved={(app) => {
          // Creation now finishes with the first publish (wizard step 5
          // picks the target cluster); the draft edit goes to the detail
          // page for the first publish too.
          void refetch()
          navigate(withSpaceParam(`/applications/${app.name}`, currentSpace))
        }}
      />

      {firstPublishTarget && (
        <DeployDialog
          app={firstPublishTarget}
          firstPublish={true}
          open={!wizardOpen}
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setFirstPublishTarget(null)
          }}
          onEditConfig={() => {
            setWizardMode('draft')
            setWizardOpen(true)
          }}
        />
      )}

      <Dialog
        open={deleteTarget !== null}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) setDeleteTarget(null)
        }}
      >
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle className="text-destructive">
              {t('applications.list.deleteTitle', {
                name: deleteTarget?.name ?? '',
              })}
            </DialogTitle>
            <DialogDescription>
              {deleteTarget && !hasDeployedConfig(deleteTarget)
                ? t('applications.list.deleteDescription')
                : t('applications.list.deleteBlocked')}
            </DialogDescription>
          </DialogHeader>
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={() => setDeleteTarget(null)}>
              {t('common.actions.cancel')}
            </Button>
            <Button
              variant="destructive"
              disabled={
                deleting ||
                (deleteTarget ? hasDeployedConfig(deleteTarget) : true)
              }
              onClick={() => void confirmDelete()}
            >
              {t('common.actions.delete')}
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      <Dialog
        open={describeTarget !== null}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) setDescribeTarget(null)
        }}
      >
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>
              {t('applications.list.editDescriptionTitle', {
                name: describeTarget?.name ?? '',
              })}
            </DialogTitle>
            <DialogDescription className="sr-only">
              {t('applications.list.editDescription')}
            </DialogDescription>
          </DialogHeader>
          <div>
            <Label>{t('applications.wizard.description')}</Label>
            <Input
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              className="mt-1.5"
            />
          </div>
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={() => setDescribeTarget(null)}>
              {t('common.actions.cancel')}
            </Button>
            <Button
              disabled={savingDescription}
              onClick={() => void saveDescription()}
            >
              {t('common.actions.confirm')}
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      {isLoading && (
        <div className="py-6 text-center text-sm text-muted-foreground">
          {t('common.messages.loading')}
        </div>
      )}
    </div>
  )
}

// One legend entry: the shared dot glyph plus its label.
function LegendItem({
  state,
  label,
}: {
  state: ApplicationDotState
  label: string
}) {
  return (
    <span className="flex items-center gap-1.5">
      <StatusDotGlyph state={state} />
      {label}
    </span>
  )
}

// Connection state of one cluster in the filter chips / picker rows: green
// when reachable, the shared red-on-gray unreachable marker otherwise.
function ClusterHealthDot({ cluster }: { cluster: string }) {
  const { t } = useTranslation()
  const { clusters, isLoading: clustersLoading } = useCluster()
  // While the cluster list is loading, health cannot be judged; pass
  // undefined so the dot does not flash the unreachable marker on bootstrap.
  const knownClusters = clustersLoading ? undefined : clusters
  const health = clusterHealth(cluster, knownClusters)
  return (
    <StatusDotGlyph
      state={health === 'ok' ? 'synced' : 'clusterUnreachable'}
      title={clusterHealthLabel(cluster, knownClusters, t)}
    />
  )
}

// Multi-select cluster filter of the toolbar (design §11), used once the
// cluster set outgrows the inline chips: one trigger styled like the space
// picker, a popover list over the current scope's clusters, and a clear
// entry. Empty selection means all clusters.
function ClusterFilterPicker({
  clusters,
  selected,
  onChange,
}: {
  clusters: string[]
  selected: string[]
  onChange: (next: string[]) => void
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)

  const summary =
    selected.length === 0
      ? t('applications.list.allClusters')
      : selected.length === 1
        ? selected[0]
        : t('applications.list.nClusters', { count: selected.length })

  const toggle = (name: string) =>
    onChange(
      selected.includes(name)
        ? selected.filter((c) => c !== name)
        : [...selected, name]
    )

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          className="h-9 max-w-56 gap-1.5 px-3"
          title={t('applications.list.clusters')}
        >
          <Server className="h-4 w-4 text-primary" />
          <span className="text-sm text-muted-foreground">
            {t('applications.list.clusters')}
          </span>
          <span className="max-w-32 truncate text-sm font-medium">
            {summary}
          </span>
          <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-56 p-2">
        {clusters.map((name) => {
          const active = selected.includes(name)
          return (
            <button
              key={name}
              type="button"
              data-active={active}
              onClick={() => toggle(name)}
              className={`flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-sm transition-colors hover:bg-accent ${
                active
                  ? 'bg-accent font-medium text-foreground'
                  : 'text-muted-foreground'
              }`}
            >
              <ClusterHealthDot cluster={name} />
              <span className="truncate font-mono text-xs">{name}</span>
              {active && (
                <Check className="ml-auto h-4 w-4 shrink-0 text-primary" />
              )}
            </button>
          )
        })}
        {clusters.length === 0 && (
          <div className="px-2 py-2 text-xs text-muted-foreground">
            {t('applications.list.allClusters')}
          </div>
        )}
        {selected.length > 0 && (
          <div className="mt-1 border-t pt-1">
            <Button
              variant="ghost"
              size="sm"
              className="w-full justify-start text-xs text-muted-foreground"
              onClick={() => onChange([])}
            >
              {t('applications.list.clearFilter')}
            </Button>
          </div>
        )}
      </PopoverContent>
    </Popover>
  )
}
