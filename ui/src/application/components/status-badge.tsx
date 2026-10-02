import {
  applicationStatusLabel,
  applicationStatusStyles,
} from '@/application/lib'
import { ApplicationStatusSummary } from '@/application/types'
import { useTranslation } from 'react-i18next'

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
