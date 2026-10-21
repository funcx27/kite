// Seed notifications for the frontend-only notification center; covers every
// type in both read states. Entries are newest-first, the order the inbox and
// bell render. Timestamps are generated relative to now so the relative-time
// display stays sensible. Titles and bodies are i18n keys
// (notifications.item.*) so the demo text stays translatable.
export type NotificationType = 'pipeline' | 'approval' | 'alert' | 'cert'

export interface LocalizedText {
  key: string
  values?: Record<string, string | number>
}

export interface NotificationItem {
  id: string
  type: NotificationType
  title: LocalizedText
  body: LocalizedText
  targetApp?: string
  // Mock-only: the inbox links the app inside this space; the real item
  // would carry the app reference itself.
  targetSpace?: string
  createdAt: string
  read: boolean
}

const minutesAgo = (minutes: number) =>
  new Date(Date.now() - minutes * 60 * 1000).toISOString()

export const mockNotifications: NotificationItem[] = [
  {
    id: 'notification-api-gateway',
    type: 'alert',
    title: { key: 'alertNotReady', values: { app: 'api-gateway' } },
    body: {
      key: 'alertNotReadyBody',
      values: { app: 'api-gateway' },
    },
    targetApp: 'api-gateway',
    targetSpace: 'team-alpha',
    createdAt: minutesAgo(18),
    read: false,
  },
  {
    id: 'notification-pipeline-42',
    type: 'pipeline',
    title: {
      key: 'pipelineFailed',
      values: { name: 'deploy-pipeline', run: 42 },
    },
    body: {
      key: 'pipelineFailedBody',
      values: { name: 'deploy-pipeline', run: 42 },
    },
    targetSpace: 'team-alpha',
    createdAt: minutesAgo(30),
    read: false,
  },
  {
    id: 'notification-approval-prod',
    type: 'approval',
    title: { key: 'approvalRequired' },
    body: {
      key: 'approvalRequiredBody',
      values: { app: 'web-console', env: 'prod' },
    },
    targetApp: 'web-console',
    targetSpace: 'team-alpha',
    createdAt: minutesAgo(55),
    read: false,
  },
  {
    id: 'notification-admin-portal-recovered',
    type: 'alert',
    title: { key: 'alertRecovered', values: { app: 'admin-portal' } },
    body: { key: 'alertRecoveredBody', values: { app: 'admin-portal' } },
    targetApp: 'admin-portal',
    targetSpace: 'team-alpha',
    createdAt: minutesAgo(90),
    read: true,
  },
  {
    id: 'notification-cert-wildcard',
    type: 'cert',
    title: { key: 'certExpiring' },
    body: {
      key: 'certExpiringBody',
      values: { cert: 'internal-wildcard', days: 13 },
    },
    createdAt: minutesAgo(240),
    read: false,
  },
  {
    id: 'notification-pipeline-41',
    type: 'pipeline',
    title: {
      key: 'pipelineSucceeded',
      values: { name: 'deploy-pipeline', run: 41 },
    },
    body: {
      key: 'pipelineSucceededBody',
      values: { name: 'deploy-pipeline', run: 41 },
    },
    targetSpace: 'team-alpha',
    createdAt: minutesAgo(300),
    read: true,
  },
  {
    id: 'notification-docs-site-drift',
    type: 'alert',
    title: { key: 'alertDrift', values: { app: 'docs-site' } },
    body: { key: 'alertDriftBody', values: { app: 'docs-site' } },
    targetApp: 'docs-site',
    targetSpace: 'team-alpha',
    createdAt: minutesAgo(1440),
    read: true,
  },
  {
    id: 'notification-approval-staging',
    type: 'approval',
    title: { key: 'approvalApproved' },
    body: {
      key: 'approvalApprovedBody',
      values: { app: 'docs-site', env: 'staging' },
    },
    targetApp: 'docs-site',
    targetSpace: 'team-alpha',
    createdAt: minutesAgo(2880),
    read: true,
  },
]
