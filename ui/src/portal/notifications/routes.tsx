import type { RouteObject } from 'react-router-dom'

import { NotificationsPage } from './page'

import './i18n'

export const notificationsRoutes: RouteObject[] = [
  {
    path: 'notifications',
    element: <NotificationsPage />,
  },
]
