import { useTranslation } from 'react-i18next'

import { ApplicationStatusSummary } from '@/types/application'
import {
  applicationStatusLabel,
  applicationStatusStyles,
} from '@/lib/applications'
import { cn } from '@/lib/utils'

export function ApplicationStatusChip({
  status,
  className,
}: {
  status: ApplicationStatusSummary
  className?: string
}) {
  const { t } = useTranslation()
  const style = applicationStatusStyles[status || 'undeployed']
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 whitespace-nowrap rounded-full border px-2 py-0.5 text-xs font-medium',
        style?.chip,
        className
      )}
    >
      <span className={cn('h-2 w-2 shrink-0 rounded-full', style?.dot)} />
      {applicationStatusLabel(status, t)}
    </span>
  )
}
