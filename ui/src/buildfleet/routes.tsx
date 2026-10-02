import type { RouteObject } from 'react-router-dom'

import { BuildFleetPage } from './page'

import './i18n'

export const buildfleetRoutes: RouteObject[] = [
  {
    path: 'buildfleet',
    element: <BuildFleetPage />,
  },
]
