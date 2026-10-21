// Demo dataset for the frontend-only monitoring center; deliberately covers
// every severity and alert state. Timestamps are generated relative to now so
// the relative-time display stays sensible.
import type { MonitoringAlert, SpaceHealthSummary } from './types'

const minutesAgo = (minutes: number) =>
  new Date(Date.now() - minutes * 60 * 1000).toISOString()

export const mockMonitoringAlerts: MonitoringAlert[] = [
  {
    id: 'alert-api-gateway',
    severity: 'critical',
    state: 'active',
    sourceType: 'appNotReady',
    target: 'api-gateway',
    space: 'team-alpha',
    cluster: 'prod-cluster',
    detail: { key: 'appNotReady', values: { ready: 2, total: 3 } },
    triggeredAt: minutesAgo(18),
    count: 3,
  },
  {
    id: 'alert-docs-site-drift',
    severity: 'warning',
    state: 'active',
    sourceType: 'configDrift',
    target: 'docs-site',
    space: 'team-alpha',
    cluster: 'staging-cluster',
    detail: { key: 'configDrift' },
    triggeredAt: minutesAgo(65),
    count: 1,
  },
  {
    id: 'alert-internal-wildcard',
    severity: 'warning',
    state: 'active',
    sourceType: 'certExpiring',
    target: 'internal-wildcard',
    space: 'platform',
    cluster: 'prod-cluster',
    detail: { key: 'certExpiring', values: { days: 13 } },
    triggeredAt: minutesAgo(240),
    count: 1,
  },
  {
    id: 'alert-admin-portal-restarts',
    severity: 'critical',
    state: 'recovered',
    sourceType: 'restartStorm',
    target: 'admin-portal',
    space: 'team-alpha',
    cluster: 'prod-cluster',
    detail: { key: 'restartStorm', values: { count: 4, minutes: 5 } },
    triggeredAt: minutesAgo(90),
    count: 4,
  },
  {
    id: 'alert-deploy-pipeline',
    severity: 'info',
    state: 'active',
    sourceType: 'pipelineFailed',
    target: 'deploy-pipeline',
    space: 'team-alpha',
    cluster: 'prod-cluster',
    detail: { key: 'pipelineFailed', values: { run: 42 } },
    triggeredAt: minutesAgo(32),
    count: 1,
  },
  {
    id: 'alert-grafana',
    severity: 'warning',
    state: 'acknowledged',
    sourceType: 'appNotReady',
    target: 'grafana',
    space: 'platform',
    cluster: 'prod-cluster',
    detail: { key: 'appNotReady', values: { ready: 1, total: 2 } },
    triggeredAt: minutesAgo(600),
    count: 2,
  },
  {
    id: 'alert-web-console-drift',
    severity: 'warning',
    state: 'acknowledged',
    sourceType: 'configDrift',
    target: 'web-console',
    space: 'team-alpha',
    cluster: 'staging-cluster',
    detail: { key: 'configDrift' },
    triggeredAt: minutesAgo(1440),
    count: 1,
  },
]

export const mockSpaceHealth: SpaceHealthSummary = {
  totalApps: 12,
  unhealthyApps: 1,
  driftedConfigs: 2,
  expiringCerts: 1,
}
