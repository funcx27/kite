// Types mirror the planned monitoring API contract: one event stream of
// alerts with a space/platform health summary.
export type AlertSeverity = 'critical' | 'warning' | 'info'

export type AlertState = 'active' | 'recovered' | 'acknowledged'

export type AlertSourceType =
  | 'appNotReady'
  | 'configDrift'
  | 'certExpiring'
  | 'restartStorm'
  | 'pipelineFailed'

// One-sentence cause, stored as an i18n key (monitoring.detail.*) plus
// interpolation values so the demo text stays translatable.
export interface AlertDetail {
  key: AlertSourceType
  values?: Record<string, string | number>
}

export interface MonitoringAlert {
  id: string
  severity: AlertSeverity
  state: AlertState
  sourceType: AlertSourceType
  target: string
  space: string
  cluster: string
  detail: AlertDetail
  triggeredAt: string
  count: number
}

export interface SpaceHealthSummary {
  totalApps: number
  unhealthyApps: number
  driftedConfigs: number
  expiringCerts: number
}
