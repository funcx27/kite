import { SpaceQuotaSettings } from '@/application/components/space-quota-settings'
import { useSpaces } from '@/platform/space-context'
import { BuildProvidersManagement } from '@/workflow/components/build-providers-management'
import { BuildRuntimeSettings } from '@/workflow/components/build-runtime-settings'
import { CredentialsManagement } from '@/workflow/components/credentials-management'
import { SpaceBuildSettings } from '@/workflow/components/space-build-settings'
import { useTranslation } from 'react-i18next'
import { Link, useSearchParams } from 'react-router-dom'

import { usePageTitle } from '@/hooks/use-page-title'
import { Button } from '@/components/ui/button'

// Application-mode settings (design §11). The URL decides the view:
// ?space=<name> shows that space's settings (resource quotas, build
// settings), the bare path shows the global sections. The header space
// picker writes ?space= on switch, so picking a space while on this page
// lands in its settings view.
export function ApplicationSettingsPage() {
  const { t } = useTranslation()
  const { currentSpace } = useSpaces()
  const [searchParams] = useSearchParams()
  usePageTitle(t('nav.settings'))

  const space = searchParams.get('space') || null

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <h1 className="text-3xl">{t('nav.settings')}</h1>
          <p className="text-muted-foreground">
            {t('applicationSettings.description')}
          </p>
        </div>
        {space !== null ? (
          <Button variant="ghost" size="sm" asChild>
            <Link to="/application-settings">
              {t('applicationSettings.viewGlobal')}
            </Link>
          </Button>
        ) : (
          currentSpace !== null && (
            <Button variant="ghost" size="sm" asChild>
              <Link
                to={`/application-settings?space=${encodeURIComponent(currentSpace)}`}
              >
                {t('applicationSettings.viewSpace', { name: currentSpace })}
              </Link>
            </Button>
          )
        )}
      </div>
      {space !== null ? (
        <div className="space-y-6">
          <SpaceQuotaSettings space={space} />
          <SpaceBuildSettings space={space} />
        </div>
      ) : (
        <div className="space-y-6">
          <CredentialsManagement />
          <BuildProvidersManagement />
          <BuildRuntimeSettings />
        </div>
      )}
    </div>
  )
}
