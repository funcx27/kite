import { useMemo } from 'react'
import { BuildProvidersManagement } from '@/workflow/components/build-providers-management'
import { BuildRuntimeSettings } from '@/workflow/components/build-runtime-settings'
import { CredentialsManagement } from '@/workflow/components/credentials-management'
import { SpaceBuildSettings } from '@/workflow/components/space-build-settings'
import { useTranslation } from 'react-i18next'

import { usePageTitle } from '@/hooks/use-page-title'
import { ResponsiveTabs } from '@/components/ui/responsive-tabs'

// Application-mode settings (design §11): space settings lead since they are
// the everyday surface, the global sections sit behind the second tab. The
// active tab lives in ?tab= so /application-settings?tab=space&space=foo is
// a shareable deep link.
export function ApplicationSettingsPage() {
  const { t } = useTranslation()
  usePageTitle(t('nav.settings'))

  const tabs = useMemo(
    () => [
      {
        value: 'space',
        label: t('applicationSettings.tabs.space'),
        content: <SpaceBuildSettings />,
      },
      {
        value: 'global',
        label: t('applicationSettings.tabs.global'),
        content: (
          <div className="space-y-6">
            <CredentialsManagement />
            <BuildProvidersManagement />
            <BuildRuntimeSettings />
          </div>
        ),
      },
    ],
    [t]
  )

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="text-3xl">{t('nav.settings')}</h1>
        <p className="text-muted-foreground">
          {t('applicationSettings.description')}
        </p>
      </div>
      <ResponsiveTabs tabs={tabs} />
    </div>
  )
}
