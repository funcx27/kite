// Identity markers and status metadata shared by the application module UI
// (design §5/§3).

import type { ApplicationStatusSummary } from '@/types/application'

export const SPACE_LABEL = 'kite.application/space'
export const SPACE_LABEL_SELECTOR = `${SPACE_LABEL}=true`
export const APP_NAME_LABEL = 'kite.application/name'

export type WorkMode = 'application' | 'professional'

export const APPLICATION_STATUSES = [
  'synced',
  'drift',
  'conflict',
  'merged',
  'undeployed',
  'error',
] as const

// Color of a config StatusSummary value; the dot carries the color so the
// list chips, tab dots and overview bar share one rendering (design §11).
export const applicationStatusStyles: Record<
  string,
  { dot: string; chip: string }
> = {
  synced: {
    dot: 'bg-green-500',
    chip: 'border-green-200 bg-green-50 text-green-700 dark:border-green-800 dark:bg-green-950 dark:text-green-400',
  },
  merged: {
    dot: 'bg-sky-500',
    chip: 'border-sky-200 bg-sky-50 text-sky-700 dark:border-sky-800 dark:bg-sky-950 dark:text-sky-400',
  },
  drift: {
    dot: 'bg-amber-500',
    chip: 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-400',
  },
  conflict: {
    dot: 'bg-amber-500',
    chip: 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-400',
  },
  error: {
    dot: 'bg-red-500',
    chip: 'border-red-200 bg-red-50 text-red-700 dark:border-red-800 dark:bg-red-950 dark:text-red-400',
  },
  undeployed: {
    dot: 'bg-zinc-400',
    chip: 'border-zinc-200 bg-zinc-50 text-zinc-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-400',
  },
}

export function applicationStatusLabel(
  status: ApplicationStatusSummary,
  t: (key: string, opts?: Record<string, unknown>) => string
): string {
  return t(`applications.status.${status || 'undeployed'}`)
}
