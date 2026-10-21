import { useMemo, useState } from 'react'
import { mockMonitoringAlerts, mockSpaceHealth } from '@/portal/monitoring/mock'
import type {
  AlertSeverity,
  AlertState,
  MonitoringAlert,
} from '@/portal/monitoring/types'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'

import { cn, getAge } from '@/lib/utils'
import { usePageTitle } from '@/hooks/use-page-title'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

// Frontend mock: acknowledging mutates local state only; the real page
// derives the active-alert count from the alerting API.
const SEVERITY_DOT_STYLES: Record<AlertSeverity, string> = {
  critical: 'bg-red-500',
  warning: 'bg-amber-500',
  info: 'bg-sky-500',
}

const TONE_STYLES = {
  amber:
    'border-amber-500/50 dark:border-amber-500/50 [&>span:first-child]:text-amber-600 dark:[&>span:first-child]:text-amber-400',
  red: 'border-red-500/50 dark:border-red-500/50 [&>span:first-child]:text-red-600 dark:[&>span:first-child]:text-red-400',
} as const

// Summary chip: bold count plus its label, same shape as the network page.
function SummaryChip({
  value,
  label,
  tone,
}: {
  value: number
  label: string
  tone?: keyof typeof TONE_STYLES
}) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-xs',
        tone && TONE_STYLES[tone]
      )}
    >
      <span className="font-semibold">{value}</span>
      <span className="text-muted-foreground">{label}</span>
    </span>
  )
}

// Dot + text chip, matching the network page's cluster status style.
function SeverityChip({ severity }: { severity: AlertSeverity }) {
  const { t } = useTranslation()
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full border bg-muted/40 px-2 py-0.5 text-xs">
      <span
        className={cn(
          'inline-block h-2 w-2 rounded-full',
          SEVERITY_DOT_STYLES[severity]
        )}
      />
      {t(`monitoring.severity.${severity}`)}
    </span>
  )
}

function StateBadge({ state }: { state: AlertState }) {
  const { t } = useTranslation()
  if (state === 'recovered') {
    return (
      <Badge
        variant="outline"
        className="border-emerald-500/50 text-emerald-600 dark:text-emerald-400"
      >
        {t('monitoring.state.recovered')}
      </Badge>
    )
  }
  if (state === 'acknowledged') {
    return (
      <Badge variant="secondary">{t('monitoring.state.acknowledged')}</Badge>
    )
  }
  return <Badge>{t('monitoring.state.active')}</Badge>
}

interface FilterGroupProps<Value extends string> {
  label: string
  value: Value
  options: readonly Value[]
  renderLabel: (value: Value) => string
  onChange: (value: Value) => void
}

function FilterGroup<Value extends string>({
  label,
  value,
  options,
  renderLabel,
  onChange,
}: FilterGroupProps<Value>) {
  return (
    <div className="flex items-center gap-1.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <div className="flex items-center gap-0.5 rounded-lg border p-0.5">
        {options.map((option) => (
          <Button
            key={option}
            variant={value === option ? 'secondary' : 'ghost'}
            size="sm"
            className="h-6 px-2 text-xs"
            onClick={() => onChange(option)}
          >
            {renderLabel(option)}
          </Button>
        ))}
      </div>
    </div>
  )
}

export function MonitoringCenterPage() {
  const { t } = useTranslation()
  usePageTitle(t('monitoring.title'))
  const [alerts, setAlerts] = useState<MonitoringAlert[]>(mockMonitoringAlerts)
  const [severityFilter, setSeverityFilter] = useState<AlertSeverity | 'all'>(
    'all'
  )
  const [stateFilter, setStateFilter] = useState<AlertState | 'all'>('all')

  const filtered = useMemo(
    () =>
      alerts.filter(
        (alert) =>
          (severityFilter === 'all' || alert.severity === severityFilter) &&
          (stateFilter === 'all' || alert.state === stateFilter)
      ),
    [alerts, severityFilter, stateFilter]
  )

  const activeCount = alerts.filter((alert) => alert.state === 'active').length

  const acknowledge = (id: string) => {
    setAlerts((prev) =>
      prev.map((alert) =>
        alert.id === id ? { ...alert, state: 'acknowledged' as const } : alert
      )
    )
    toast.success(t('monitoring.toast.acknowledged'))
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="space-y-1.5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 className="text-2xl font-bold">{t('monitoring.title')}</h1>
            <p className="text-sm text-muted-foreground">
              {t('monitoring.description')}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <FilterGroup
              label={t('monitoring.filter.severity')}
              value={severityFilter}
              options={['all', 'critical', 'warning', 'info'] as const}
              renderLabel={(value) => t(`monitoring.filter.${value}`)}
              onChange={setSeverityFilter}
            />
            <FilterGroup
              label={t('monitoring.filter.state')}
              value={stateFilter}
              options={['all', 'active', 'recovered', 'acknowledged'] as const}
              renderLabel={(value) => t(`monitoring.filter.${value}`)}
              onChange={setStateFilter}
            />
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <SummaryChip
            value={mockSpaceHealth.totalApps}
            label={t('monitoring.summary.apps')}
          />
          <SummaryChip
            value={mockSpaceHealth.unhealthyApps}
            label={t('monitoring.summary.unhealthy')}
            tone="amber"
          />
          <SummaryChip
            value={mockSpaceHealth.driftedConfigs}
            label={t('monitoring.summary.drifted')}
          />
          <SummaryChip
            value={mockSpaceHealth.expiringCerts}
            label={t('monitoring.summary.certExpiring')}
          />
          <SummaryChip
            value={activeCount}
            label={t('monitoring.summary.activeAlerts')}
            tone="red"
          />
        </div>
      </div>

      {filtered.length === 0 ? (
        <div className="flex flex-col items-center gap-1 py-12 text-center">
          <p className="text-sm font-medium text-muted-foreground">
            {t('monitoring.emptyTitle')}
          </p>
          <p className="text-sm text-muted-foreground">
            {t('monitoring.emptyDescription')}
          </p>
        </div>
      ) : (
        <Card>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('monitoring.columns.severity')}</TableHead>
                  <TableHead>{t('monitoring.columns.state')}</TableHead>
                  <TableHead>{t('monitoring.columns.source')}</TableHead>
                  <TableHead>{t('monitoring.columns.target')}</TableHead>
                  <TableHead>{t('monitoring.columns.cluster')}</TableHead>
                  <TableHead>{t('monitoring.columns.detail')}</TableHead>
                  <TableHead>{t('monitoring.columns.triggeredAt')}</TableHead>
                  <TableHead className="text-right">
                    {t('monitoring.columns.count')}
                  </TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((alert) => (
                  <TableRow key={alert.id}>
                    <TableCell>
                      <SeverityChip severity={alert.severity} />
                    </TableCell>
                    <TableCell>
                      <StateBadge state={alert.state} />
                    </TableCell>
                    <TableCell className="text-sm">
                      {t(`monitoring.source.${alert.sourceType}`)}
                    </TableCell>
                    <TableCell>
                      <Link
                        to={`/applications/${alert.space}/${alert.target}`}
                        className="text-primary hover:underline"
                      >
                        {alert.target}
                      </Link>
                    </TableCell>
                    <TableCell>
                      <span className="font-mono text-xs">{alert.cluster}</span>
                    </TableCell>
                    <TableCell className="max-w-xs whitespace-normal text-sm text-muted-foreground">
                      {t(
                        `monitoring.detail.${alert.detail.key}`,
                        alert.detail.values
                      )}
                    </TableCell>
                    <TableCell>
                      <span className="text-xs text-muted-foreground">
                        {t('common.messages.timeAgo', {
                          time: getAge(alert.triggeredAt),
                        })}
                      </span>
                    </TableCell>
                    <TableCell className="text-right text-sm tabular-nums">
                      {alert.count}
                    </TableCell>
                    <TableCell>
                      {alert.state === 'active' && (
                        <Button
                          variant="outline"
                          size="sm"
                          className="h-7"
                          onClick={() => acknowledge(alert.id)}
                        >
                          {t('monitoring.actions.acknowledge')}
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
