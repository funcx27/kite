import { ApplicationModeRoute } from '@/platform/home-route'
import type { RouteObject } from 'react-router-dom'

import { ApplicationDetailPage } from './pages/application-detail-page'
import { ApplicationListPage } from './pages/application-list-page'
import { ApplicationPodPage } from './pages/application-pod-page'

import './i18n'

// Application module routes: application-mode only, kept ahead of the
// :resource wildcards in the host routes (design §11).
export const applicationRoutes: RouteObject[] = [
  {
    path: 'applications',
    element: (
      <ApplicationModeRoute>
        <ApplicationListPage />
      </ApplicationModeRoute>
    ),
  },
  {
    path: 'applications/:name',
    element: (
      <ApplicationModeRoute>
        <ApplicationDetailPage />
      </ApplicationModeRoute>
    ),
  },
  {
    path: 'applications/:name/clusters/:cluster',
    element: (
      <ApplicationModeRoute>
        <ApplicationDetailPage />
      </ApplicationModeRoute>
    ),
  },
  {
    path: 'applications/:name/clusters/:cluster/pods/:pod',
    element: (
      <ApplicationModeRoute>
        <ApplicationPodPage />
      </ApplicationModeRoute>
    ),
  },
]
