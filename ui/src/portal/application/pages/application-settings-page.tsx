import { CertificatesManagement } from '@/portal/application/components/certificates-management'
import { SpaceQuotaSettings } from '@/portal/application/components/space-quota-settings'
import { KcrossManagement } from '@/portal/kcross/kcross-management'
import { useSpaces, useSyncSpaceParam } from '@/portal/space-context'
import { BuildProvidersManagement } from '@/portal/workflow/components/build-providers-management'
import { BuildRuntimeSettings } from '@/portal/workflow/components/build-runtime-settings'
import { CredentialsManagement } from '@/portal/workflow/components/credentials-management'
import { SpaceBuildSettings } from '@/portal/workflow/components/space-build-settings'
import { useTranslation } from 'react-i18next'
import { Link, useSearchParams } from 'react-router-dom'

import { usePageTitle } from '@/hooks/use-page-title'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

// Application-mode settings (design §11). The view follows the space: a
// ?space=<name> shows that space's settings (resource quotas, build
// settings); the bare path follows the header picker's active space. The
// view-global button clears the active space itself (setCurrentSpace(null)),
// so the picker switches to "all spaces" instead of only this page going
// global while the header still shows the space. An explicit empty ?space=
// (bookmark) still renders the global view.
export function ApplicationSettingsPage() {
  const { t } = useTranslation()
  const { currentSpace, setCurrentSpace } = useSpaces()
  const [searchParams] = useSearchParams()
  useSyncSpaceParam()
  usePageTitle(t('applicationSettings.settings'))

  const rawSpace = searchParams.get('space')
  const space = rawSpace === null ? currentSpace : rawSpace || null

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <h1 className="text-3xl">{t('applicationSettings.settings')}</h1>
          <p className="text-muted-foreground">
            {t('applicationSettings.description')}
          </p>
        </div>
        {space !== null ? (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setCurrentSpace(null)}
          >
            {t('applicationSettings.viewGlobal')}
          </Button>
        ) : (
          currentSpace !== null && (
            <Button variant="ghost" size="sm" asChild>
              <Link to={`/settings?space=${encodeURIComponent(currentSpace)}`}>
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
          <CertificatesManagement />
          {/* Global catalog hosted as a settings sub-page (no space scope). */}
          <Card>
            <CardHeader>
              <CardTitle>{t('network.annotationKeys.title')}</CardTitle>
              <CardDescription>
                {t('network.annotationKeys.description')}
              </CardDescription>
            </CardHeader>
            <CardContent>
              <Button variant="outline" size="sm" asChild>
                <Link to="/settings/annotation-keys">
                  {t('network.annotationKeys.open')}
                </Link>
              </Button>
            </CardContent>
          </Card>
          <BuildProvidersManagement />
          <BuildRuntimeSettings />
          <KcrossManagement />
        </div>
      )}
    </div>
  )
}
