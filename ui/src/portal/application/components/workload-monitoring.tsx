// Front-end mock of the workload monitoring view: every series, chip and
// event below is generated demo data — the real version would read
// Prometheus / metrics-server.
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Area,
  AreaChart,
  CartesianGrid,
  ReferenceLine,
  XAxis,
  YAxis,
} from 'recharts'

import { formatChartXTicks, formatDate } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  ChartConfig,
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { ChartStateWrapper, isSameDay } from '@/components/chart/chart-utils'

interface WorkloadMonitoringProps {
  namespace: string
  name: string
  cluster: string
}

interface MetricPoint {
  timestamp: string
  [series: string]: number | string
}

interface MockEvent {
  minutesAgo: number
  kind: 'warning' | 'normal' | 'deploy'
  reason: string
  // i18n key under applications.monitoring.event. plus interpolation values,
  // so the demo sentences stay translatable.
  messageKey: string
  messageValues?: Record<string, string | number>
}

interface SeriesDef {
  dataKey: string
  type: 'monotone' | 'stepAfter'
  fillId: string
}

interface RefLine {
  y: number
  colorKey: string
  label: string
}

const POINT_COUNT = 60
const MOCK_REQUESTS_CPU = 0.25
const MOCK_LIMITS_MEMORY = 512

const RANGE_MS: Record<string, number> = {
  '30m': 30 * 60_000,
  '1h': 3_600_000,
  '6h': 6 * 3_600_000,
  '24h': 24 * 3_600_000,
}

const TIME_RANGE_OPTIONS = ['30m', '1h', '6h', '24h'] as const

const REFRESH_OPTIONS = [
  { value: 0, labelKey: 'refreshOff' },
  { value: 5_000, labelKey: 'refresh5s' },
  { value: 10_000, labelKey: 'refresh10s' },
  { value: 30_000, labelKey: 'refresh30s' },
  { value: 60_000, labelKey: 'refresh60s' },
]

// Demo data — the real version would surface real Kubernetes events instead.
const MOCK_EVENTS: MockEvent[] = [
  {
    minutesAgo: 22,
    kind: 'warning',
    reason: 'BackOff',
    messageKey: 'backOff',
  },
  {
    minutesAgo: 48,
    kind: 'normal',
    reason: 'Pulled',
    messageKey: 'pulled',
    messageValues: { image: 'registry.local/demo/app:v1.4.2' },
  },
  {
    minutesAgo: 65,
    kind: 'normal',
    reason: 'Scheduled',
    messageKey: 'scheduled',
    messageValues: { node: 'worker-3' },
  },
  {
    minutesAgo: 128,
    kind: 'deploy',
    reason: 'RollingUpdate',
    messageKey: 'rollingUpdate',
    messageValues: { from: 'v1.4.1', to: 'v1.4.2', replicas: 3 },
  },
  {
    minutesAgo: 175,
    kind: 'warning',
    reason: 'Unhealthy',
    messageKey: 'unhealthy',
    messageValues: { status: 503 },
  },
  {
    minutesAgo: 262,
    kind: 'deploy',
    reason: 'RollingUpdate',
    messageKey: 'rollingUpdate',
    messageValues: { from: 'v1.4.0', to: 'v1.4.1', replicas: 3 },
  },
  {
    minutesAgo: 320,
    kind: 'normal',
    reason: 'ScalingReplicaSet',
    messageKey: 'scalingReplicaSet',
    messageValues: { from: 2, to: 3 },
  },
]

// Deterministic pseudo-random in [0, 1) from an index — Math.random would
// reshuffle the mock curves on every re-render.
function seeded(index: number, seed: number): number {
  const x = Math.sin(index * 12.9898 + seed * 78.233) * 43758.5453
  return x - Math.floor(x)
}

function identitySeed(identity: string): number {
  let hash = 0
  for (let i = 0; i < identity.length; i++) {
    hash = (hash * 31 + identity.charCodeAt(i)) % 9973
  }
  return hash
}

// Demo data — the real version would query Prometheus / metrics-server for
// these series.
function buildMockSeries(seed: number, rangeMs: number) {
  const now = Date.now()
  const stepMs = rangeMs / (POINT_COUNT - 1)
  const dipStart = 20 + Math.floor(seeded(1, seed) * 15)
  const restartBase = 1 + Math.floor(seeded(2, seed) * 3)
  const restartA = 12 + Math.floor(seeded(3, seed) * 12)
  const restartB = 38 + Math.floor(seeded(4, seed) * 12)

  const cpu: MetricPoint[] = []
  const memory: MetricPoint[] = []
  const replicas: MetricPoint[] = []
  const restarts: MetricPoint[] = []
  const traffic: MetricPoint[] = []
  for (let i = 0; i < POINT_COUNT; i++) {
    const timestamp = new Date(
      now - (POINT_COUNT - 1 - i) * stepMs
    ).toISOString()
    cpu.push({
      timestamp,
      usage: Number(
        (0.14 + 0.08 * Math.sin(i / 6 + seed) + 0.06 * seeded(i, seed)).toFixed(
          3
        )
      ),
    })
    memory.push({
      timestamp,
      usage: Math.round(
        230 + 70 * Math.sin(i / 9 + seed) + 40 * seeded(i + 7, seed)
      ),
    })
    replicas.push({
      timestamp,
      desired: 3,
      ready: i >= dipStart && i <= dipStart + 6 ? 2 : 3,
    })
    restarts.push({
      timestamp,
      total: restartBase + (i >= restartA ? 1 : 0) + (i >= restartB ? 1 : 0),
    })
    traffic.push({
      timestamp,
      qps: Number(
        (45 + 15 * Math.sin(i / 6 + seed) + 8 * seeded(i + 3, seed)).toFixed(1)
      ),
      p95: Number(
        (
          70 +
          25 * Math.sin(i / 8 + seed + 2) +
          20 * seeded(i + 5, seed)
        ).toFixed(1)
      ),
      errorRate: Number(
        Math.max(
          0,
          0.6 +
            0.5 * Math.sin(i / 11 + seed) +
            0.4 * (seeded(i + 11, seed) - 0.5)
        ).toFixed(2)
      ),
    })
  }
  return { cpu, memory, replicas, restarts, traffic }
}

function SummaryChip({ label, value }: { label: string; value: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs">
      <span className="font-semibold">{value}</span>
      <span className="text-muted-foreground">{label}</span>
    </span>
  )
}

function SeriesChartBody({
  config,
  data,
  series,
  refLines,
  yTickFormatter,
  integerY,
  showLegend,
  syncId,
}: {
  config: ChartConfig
  data: MetricPoint[]
  series: SeriesDef[]
  refLines?: RefLine[]
  yTickFormatter?: (value: number) => string
  integerY?: boolean
  showLegend?: boolean
  syncId: string
}) {
  const sameDay = isSameDay(data)
  // Keep request/limits reference lines inside the y range.
  const refMax = (refLines ?? []).reduce(
    (max, line) => Math.max(max, line.y),
    0
  )
  return (
    <ChartContainer config={config} className="h-[250px] w-full">
      <AreaChart data={data} syncId={syncId}>
        <defs>
          {series.map(({ dataKey, fillId }) => (
            <linearGradient
              key={dataKey}
              id={fillId}
              x1="0"
              y1="0"
              x2="0"
              y2="1"
            >
              <stop
                offset="5%"
                stopColor={`var(--color-${dataKey})`}
                stopOpacity={0.4}
              />
              <stop
                offset="95%"
                stopColor={`var(--color-${dataKey})`}
                stopOpacity={0.05}
              />
            </linearGradient>
          ))}
        </defs>
        <CartesianGrid vertical={false} />
        {(refLines ?? []).map((line) => (
          <ReferenceLine
            key={line.colorKey}
            y={line.y}
            stroke={`var(--color-${line.colorKey})`}
            strokeDasharray="4 4"
            label={{
              value: line.label,
              position: 'insideTopLeft',
              fontSize: 10,
              fill: `var(--color-${line.colorKey})`,
            }}
          />
        ))}
        <XAxis
          dataKey="timestamp"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          minTickGap={30}
          allowDataOverflow={true}
          tickFormatter={(value) => formatChartXTicks(value, sameDay)}
        />
        <YAxis
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          tickFormatter={yTickFormatter}
          allowDecimals={integerY ? false : undefined}
          domain={[0, (dataMax: number) => Math.max(dataMax * 1.1, refMax)]}
        />
        <ChartTooltip
          content={
            <ChartTooltipContent
              labelFormatter={(value) => formatDate(value)}
            />
          }
        />
        {showLegend && <ChartLegend content={<ChartLegendContent />} />}
        {series.map(({ dataKey, type, fillId }) => (
          <Area
            key={dataKey}
            isAnimationActive={false}
            dataKey={dataKey}
            type={type}
            fill={`url(#${fillId})`}
            stroke={`var(--color-${dataKey})`}
            strokeWidth={2}
            dot={false}
          />
        ))}
      </AreaChart>
    </ChartContainer>
  )
}

function SeriesChart(props: {
  title: string
  config: ChartConfig
  data: MetricPoint[]
  series: SeriesDef[]
  refLines?: RefLine[]
  yTickFormatter?: (value: number) => string
  integerY?: boolean
  showLegend?: boolean
  syncId: string
}) {
  return (
    <ChartStateWrapper title={props.title}>
      <SeriesChartBody {...props} />
    </ChartStateWrapper>
  )
}

export function WorkloadMonitoring({
  namespace,
  name,
  cluster,
}: WorkloadMonitoringProps) {
  const { t } = useTranslation()
  const [timeRange, setTimeRange] = useState('30m')
  const [refreshInterval, setRefreshInterval] = useState(30 * 1000)
  // Mock stand-in for auto-refetch: every tick folds into the noise seed so
  // the curves visibly move while staying deterministic per render.
  const [tick, setTick] = useState(0)

  useEffect(() => {
    if (refreshInterval === 0) return
    const id = window.setInterval(() => setTick((n) => n + 1), refreshInterval)
    return () => window.clearInterval(id)
  }, [refreshInterval])

  const seed = identitySeed(`${namespace}/${name}@${cluster}`) + tick
  const series = useMemo(
    () => buildMockSeries(seed, RANGE_MS[timeRange]),
    [seed, timeRange]
  )

  const events = useMemo(
    () =>
      MOCK_EVENTS.map((event) => ({
        ...event,
        time: new Date(Date.now() - event.minutesAgo * 60_000).toISOString(),
      })),
    []
  )
  const eventBadge = {
    warning: {
      variant: 'destructive',
      label: t('applications.monitoring.typeWarning'),
    },
    normal: {
      variant: 'outline',
      label: t('applications.monitoring.typeNormal'),
    },
    deploy: {
      variant: 'default',
      label: t('applications.monitoring.typeDeploy'),
    },
  } as const

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-2 font-mono text-xs text-muted-foreground">
          {namespace}/{name} · {cluster}
        </span>
        <SummaryChip
          label={t('applications.monitoring.readyRate')}
          value="100%"
        />
        <SummaryChip
          label={t('applications.monitoring.restarts30m')}
          value="2"
        />
        <SummaryChip
          label={t('applications.monitoring.sinceLastDeploy')}
          value="3h"
        />
        <SummaryChip
          label={t('applications.monitoring.currentImage')}
          value="v1.4.2"
        />
      </div>

      {/* Controls */}
      <div className="flex flex-col gap-4 md:flex-row md:flex-wrap">
        <div className="w-full space-y-2 md:w-auto">
          <Select value={timeRange} onValueChange={setTimeRange}>
            <SelectTrigger className="w-full md:w-[200px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {TIME_RANGE_OPTIONS.map((option) => (
                <SelectItem key={option} value={option}>
                  {t(`applications.monitoring.range${option}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="w-full space-y-2 md:w-auto">
          <Select
            value={refreshInterval.toString()}
            onValueChange={(value) => setRefreshInterval(Number(value))}
          >
            <SelectTrigger className="w-full md:w-[200px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {REFRESH_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value.toString()}>
                  {t(`applications.monitoring.${option.labelKey}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      {/* Charts */}
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <SeriesChart
          title={t('applications.monitoring.cpu')}
          syncId="workload-monitoring"
          config={{
            usage: {
              label: t('applications.monitoring.usage'),
              theme: {
                light: 'hsl(220, 70%, 50%)',
                dark: 'hsl(210, 80%, 60%)',
              },
            },
            requests: {
              label: t('applications.monitoring.requestsRef'),
              color: 'oklch(0.65 0.05 260)',
            },
          }}
          data={series.cpu}
          series={[{ dataKey: 'usage', type: 'monotone', fillId: 'fillWmCpu' }]}
          refLines={[
            {
              y: MOCK_REQUESTS_CPU,
              colorKey: 'requests',
              label: t('applications.monitoring.requestsRef'),
            },
          ]}
          yTickFormatter={(value) => value.toFixed(2)}
        />
        <SeriesChart
          title={t('applications.monitoring.memory')}
          syncId="workload-monitoring"
          config={{
            usage: {
              label: t('applications.monitoring.usage'),
              theme: {
                light: 'hsl(142, 70%, 50%)',
                dark: 'hsl(150, 80%, 60%)',
              },
            },
            limits: {
              label: t('applications.monitoring.limitsRef'),
              color: 'oklch(0.65 0.15 60)',
            },
          }}
          data={series.memory}
          series={[
            { dataKey: 'usage', type: 'monotone', fillId: 'fillWmMemory' },
          ]}
          refLines={[
            {
              y: MOCK_LIMITS_MEMORY,
              colorKey: 'limits',
              label: t('applications.monitoring.limitsRef'),
            },
          ]}
          yTickFormatter={(value) => `${Math.round(value)}Mi`}
        />
        <SeriesChart
          title={t('applications.monitoring.replicas')}
          syncId="workload-monitoring"
          config={{
            desired: {
              label: t('applications.monitoring.desired'),
              color: 'oklch(0.6 0.03 260)',
            },
            ready: {
              label: t('applications.monitoring.ready'),
              color: 'oklch(0.55 0.2 145)',
            },
          }}
          data={series.replicas}
          series={[
            { dataKey: 'desired', type: 'stepAfter', fillId: 'fillWmDesired' },
            { dataKey: 'ready', type: 'stepAfter', fillId: 'fillWmReady' },
          ]}
          integerY
          showLegend
        />
        <SeriesChart
          title={t('applications.monitoring.restarts')}
          syncId="workload-monitoring"
          config={{
            total: {
              label: t('applications.monitoring.restarts'),
              color: 'oklch(0.65 0.15 70)',
            },
          }}
          data={series.restarts}
          series={[
            { dataKey: 'total', type: 'monotone', fillId: 'fillWmRestarts' },
          ]}
          integerY
        />
        <Card className="@container/card xl:col-span-2">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              {t('applications.monitoring.ingress')}
              <span className="ml-auto text-xs font-normal text-muted-foreground">
                {t('applications.monitoring.demoNote')}
              </span>
            </CardTitle>
          </CardHeader>
          <CardContent className="px-2 sm:px-6">
            <SeriesChartBody
              syncId="workload-monitoring"
              config={{
                qps: {
                  label: t('applications.monitoring.qps'),
                  color: 'oklch(0.55 0.22 235)',
                },
                p95: {
                  label: `${t('applications.monitoring.p95Latency')} (ms)`,
                  color: 'oklch(0.65 0.18 55)',
                },
                errorRate: {
                  label: `${t('applications.monitoring.errorRate')} (%)`,
                  color: 'oklch(0.6 0.2 25)',
                },
              }}
              data={series.traffic}
              series={[
                { dataKey: 'qps', type: 'monotone', fillId: 'fillWmQps' },
                { dataKey: 'p95', type: 'monotone', fillId: 'fillWmP95' },
                {
                  dataKey: 'errorRate',
                  type: 'monotone',
                  fillId: 'fillWmError',
                },
              ]}
              showLegend
            />
          </CardContent>
        </Card>
      </div>

      {/* Mock event timeline */}
      <div className="overflow-hidden rounded-xl border bg-card">
        <div className="border-b px-5 py-3 text-sm font-semibold">
          {t('applications.monitoring.eventTimeline')}
        </div>
        <div className="flex items-center gap-3 border-b px-5 py-2 text-xs text-muted-foreground">
          <span className="w-40 shrink-0">
            {t('applications.monitoring.time')}
          </span>
          <span className="w-16 shrink-0">
            {t('applications.monitoring.type')}
          </span>
          <span className="w-28 shrink-0">
            {t('applications.monitoring.reason')}
          </span>
          <span className="min-w-0 flex-1">
            {t('applications.monitoring.message')}
          </span>
        </div>
        <div className="divide-y">
          {events.map((event) => (
            <div
              key={event.reason + event.minutesAgo}
              className="flex items-center gap-3 px-5 py-2.5 text-sm"
            >
              <span className="w-40 shrink-0 truncate font-mono text-xs text-muted-foreground">
                {formatDate(event.time)}
              </span>
              <span className="w-16 shrink-0">
                <Badge variant={eventBadge[event.kind].variant}>
                  {eventBadge[event.kind].label}
                </Badge>
              </span>
              <span className="w-28 shrink-0 truncate font-mono text-xs text-muted-foreground">
                {event.reason}
              </span>
              <span className="min-w-0 flex-1 truncate text-muted-foreground">
                {t(
                  `applications.monitoring.event.${event.messageKey}`,
                  event.messageValues
                )}
              </span>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
