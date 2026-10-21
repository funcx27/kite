import type { RouteObject } from 'react-router-dom'

import { MonitoringCenterPage } from './page'

import './i18n'

export const monitoringRoutes: RouteObject[] = [
  {
    path: 'monitoring',
    element: <MonitoringCenterPage />,
  },
]
