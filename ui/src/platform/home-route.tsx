import './i18n'

import type { ReactNode } from 'react'
import { Overview } from '@/pages/overview'
import { useWorkMode } from '@/platform/work-mode-context'
import { Navigate } from 'react-router-dom'

// Application mode makes the application workbench the homepage (no
// dashboard, design §11); the professional mode keeps the overview.
export function HomeRoute() {
  const { isApplicationMode } = useWorkMode()
  if (isApplicationMode) {
    return <Navigate to="/applications" replace />
  }
  return <Overview />
}

// The professional console does not surface the application pages (design
// §11: the two work modes do not cross-link); a direct URL bounces home.
export function ApplicationModeRoute({ children }: { children: ReactNode }) {
  const { isApplicationMode } = useWorkMode()
  if (!isApplicationMode) {
    return <Navigate to="/" replace />
  }
  return <>{children}</>
}
