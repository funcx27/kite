import type { ComponentType, ReactNode } from 'react'
import { CredentialsManagement } from '@/workflow/components/credentials-management'
import type { TFunction } from 'i18next'

import { APIKeyManagement } from './apikey-management'
import { AuditLog } from './audit-log'
import { AuthenticationManagement } from './authentication-management'
import { ClusterManagement } from './cluster-management'
import { GeneralManagement } from './general-management'
import { KcrossManagement } from './kcross-management'
import { RBACManagement } from './rbac-management'
import { TemplateManagement } from './template-management'
import { UserManagement } from './user-management'

export interface SettingsSectionDefinition {
  value: string
  labelKey: string
  defaultLabel: string
  render: () => ReactNode
}

function createSettingsSectionDefinition(
  value: string,
  labelKey: string,
  defaultLabel: string,
  Component: ComponentType
): SettingsSectionDefinition {
  return {
    value,
    labelKey,
    defaultLabel,
    render: () => <Component />,
  }
}

export const settingsSectionRegistry: SettingsSectionDefinition[] = [
  createSettingsSectionDefinition(
    'general',
    'settings.tabs.general',
    'General',
    GeneralManagement
  ),
  createSettingsSectionDefinition(
    'clusters',
    'settings.tabs.clusters',
    'Cluster',
    ClusterManagement
  ),
  createSettingsSectionDefinition(
    'kcross',
    'settings.tabs.kcross',
    'kcross',
    KcrossManagement
  ),
  createSettingsSectionDefinition(
    'credentials',
    'settings.tabs.credentials',
    'Credentials',
    CredentialsManagement
  ),
  createSettingsSectionDefinition(
    'oauth',
    'settings.tabs.oauth',
    'Authentication',
    AuthenticationManagement
  ),
  createSettingsSectionDefinition(
    'rbac',
    'settings.tabs.rbac',
    'RBAC',
    RBACManagement
  ),
  createSettingsSectionDefinition(
    'users',
    'settings.tabs.users',
    'User',
    UserManagement
  ),
  createSettingsSectionDefinition(
    'apikeys',
    'settings.tabs.apikeys',
    'API Keys',
    APIKeyManagement
  ),
  createSettingsSectionDefinition(
    'templates',
    'settings.tabs.templates',
    'Templates',
    TemplateManagement
  ),
  createSettingsSectionDefinition(
    'audit',
    'settings.tabs.audit',
    'Audit',
    AuditLog
  ),
]

export function createSettingsTabs(t: TFunction) {
  return settingsSectionRegistry.map((section) => ({
    value: section.value,
    label: t(section.labelKey, section.defaultLabel),
    content: section.render(),
  }))
}
