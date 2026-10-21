import type { RouteObject } from 'react-router-dom'

import { AnnotationKeysPage } from './annotation-keys-page'
import { NetworkPage } from './page'

import './i18n'

export const networkRoutes: RouteObject[] = [
  {
    path: 'network',
    element: <NetworkPage />,
  },
  {
    // Global settings sub-page (no space dimension).
    path: 'settings/annotation-keys',
    element: <AnnotationKeysPage />,
  },
]
