import { BuildProvidersManagement } from '@/workflow/components/build-providers-management'
import { BuildSettingsManagement } from '@/workflow/components/build-settings-management'
import { CredentialsManagement } from '@/workflow/components/credentials-management'
import { useTranslation } from 'react-i18next'

import { usePageTitle } from '@/hooks/use-page-title'

// Application-mode settings (design §11): a plain stack of sections so later
// blocks (e.g. build clusters) slot in as additional children.
export function ApplicationSettingsPage() {
  const { t } = useTranslation()
  usePageTitle(t('nav.settings'))

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="text-3xl">{t('nav.settings')}</h1>
        <p className="text-muted-foreground">
          {t('applicationSettings.description')}
        </p>
      </div>
      <CredentialsManagement />
      <BuildProvidersManagement />
      <BuildSettingsManagement />
    </div>
  )
}
