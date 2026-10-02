import { IconCircleCheck, IconCircleX, IconCopy } from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { BuildResultEvent } from '@/types/buildfleet'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

interface BuildResultCardProps {
  result: BuildResultEvent | null
  error: string | null
}

function ResultRow({
  label,
  value,
  onCopy,
}: {
  label: string
  value: string
  onCopy?: boolean
}) {
  const { t } = useTranslation()
  return (
    <div className="flex items-start gap-2 text-sm">
      <span className="w-24 shrink-0 text-muted-foreground">{label}</span>
      <span className="min-w-0 flex-1 break-all font-mono text-xs">
        {value}
      </span>
      {onCopy && (
        <button
          type="button"
          className="shrink-0 text-muted-foreground hover:text-foreground"
          onClick={() => {
            navigator.clipboard.writeText(value)
            toast.success(t('common.messages.copied'))
          }}
        >
          <IconCopy className="size-4" />
        </button>
      )}
    </div>
  )
}

export function BuildResultCard({ result, error }: BuildResultCardProps) {
  const { t } = useTranslation()

  if (!result && !error) return null

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          {error ? (
            <IconCircleX className="size-5 text-red-500" />
          ) : (
            <IconCircleCheck className="size-5 text-green-600" />
          )}
          {error ? t('buildfleet.result.failed') : t('buildfleet.result.title')}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {error ? (
          <pre className="overflow-auto whitespace-pre-wrap break-all rounded-md bg-destructive/10 p-2 font-mono text-xs text-red-600 dark:text-red-400">
            {error}
          </pre>
        ) : (
          result && (
            <>
              <ResultRow
                label={t('buildfleet.result.imageRef')}
                value={result.imageRef}
                onCopy
              />
              <ResultRow
                label={t('buildfleet.result.digest')}
                value={result.digest}
                onCopy
              />
              <ResultRow
                label={t('buildfleet.result.attempts')}
                value={String(result.attempts)}
              />
              {result.ref && (
                <ResultRow
                  label={t('buildfleet.result.ref')}
                  value={result.ref}
                />
              )}
              {result.commit && (
                <ResultRow
                  label={t('buildfleet.result.commit')}
                  value={result.commit}
                />
              )}
              {result.runtimeId && (
                <ResultRow
                  label={t('buildfleet.result.runtimeId')}
                  value={result.runtimeId}
                />
              )}
              {result.provider && (
                <ResultRow
                  label={t('buildfleet.result.provider')}
                  value={result.provider}
                />
              )}
            </>
          )
        )}
      </CardContent>
    </Card>
  )
}
