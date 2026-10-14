import type { RouteObject } from 'react-router-dom'

import { ApplicationDetailPage } from './pages/application-detail-page'
import { ApplicationListPage } from './pages/application-list-page'
import { ApplicationPodPage } from './pages/application-pod-page'
import { ApplicationSettingsPage } from './pages/application-settings-page'

import './i18n'

// Application module routes, mounted by the platform SPA root
// (src/platform/main.tsx).
export const applicationRoutes: RouteObject[] = [
  {
    path: 'applications',
    element: <ApplicationListPage />,
  },
  {
    path: 'applications/:space/:name',
    element: <ApplicationDetailPage />,
  },
  {
    path: 'applications/:space/:name/clusters/:cluster',
    element: <ApplicationDetailPage />,
  },
  {
    path: 'applications/:space/:name/clusters/:cluster/pods/:pod',
    element: <ApplicationPodPage />,
  },
  {
    path: 'application-settings',
    element: <ApplicationSettingsPage />,
  },
]
