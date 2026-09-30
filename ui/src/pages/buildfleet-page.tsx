import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { BuildForm } from '@/components/buildfleet/build-form'
import {
  BuildLogStream,
  BuildSession,
} from '@/components/buildfleet/build-log-stream'
import { BuildResultCard } from '@/components/buildfleet/build-result-card'
import { BuildStatusSection } from '@/components/buildfleet/build-status-section'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { usePageTitle } from '@/hooks/use-page-title'
import { BuildRequest, BuildResultEvent } from '@/types/buildfleet'

export function BuildFleetPage() {
  const { t } = useTranslation()
  usePageTitle(t('buildfleet.title'))

  const [session, setSession] = useState<BuildSession | null>(null)
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<BuildResultEvent | null>(null)
  const [error, setError] = useState<string | null>(null)

  const startBuildSession = (request: BuildRequest) => {
    setResult(null)
    setError(null)
    setSession({ request })
  }

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-bold">{t('buildfleet.title')}</h1>
        <p className="text-sm text-muted-foreground">
          {t('buildfleet.description')}
        </p>
      </div>

      <div className="grid grid-cols-1 gap-4 @5xl/main:grid-cols-2">
        <BuildForm running={running} onSubmit={startBuildSession} />
        <Card>
          <CardHeader>
            <CardTitle>{t('buildfleet.log.title')}</CardTitle>
            <CardDescription>{t('buildfleet.log.description')}</CardDescription>
          </CardHeader>
          <CardContent>
            <BuildLogStream
              session={session}
              onRunningChange={setRunning}
              onResult={setResult}
              onError={setError}
            />
          </CardContent>
        </Card>
      </div>

      <BuildResultCard result={result} error={error} />

      <BuildStatusSection />
    </div>
  )
}
