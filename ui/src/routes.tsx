import { createBrowserRouter } from 'react-router-dom'

import App, { StandaloneAIChatApp } from './App'
import { applicationRoutes } from './application/routes'
import { buildfleetRoutes } from './buildfleet/routes'
import { InitCheckRoute } from './components/init-check-route'
import { ProtectedRoute } from './components/protected-route'
import { getSubPath } from './lib/subpath'
import { CRListPage } from './pages/cr-list-page'
import { HelmChartDetailPage } from './pages/helm-chart-detail-page'
import { HelmChartListPage } from './pages/helm-chart-list-page'
import { InitializationPage } from './pages/initialization'
import { LoginPage } from './pages/login'
import { ResourceDetail } from './pages/resource-detail'
import { ResourceList } from './pages/resource-list'
import { SettingsPage } from './pages/settings'
import { HomeRoute } from './platform/home-route'
import { workflowRoutes } from './workflow/routes'

const subPath = getSubPath()

export const router = createBrowserRouter(
  [
    {
      path: '/setup',
      element: <InitializationPage />,
    },
    {
      path: '/login',
      element: (
        <InitCheckRoute allowIncompleteSetup>
          <LoginPage />
        </InitCheckRoute>
      ),
    },
    {
      path: '/ai-chat-box',
      element: (
        <InitCheckRoute>
          <ProtectedRoute>
            <StandaloneAIChatApp />
          </ProtectedRoute>
        </InitCheckRoute>
      ),
    },
    {
      path: '/',
      element: (
        <InitCheckRoute>
          <ProtectedRoute>
            <App />
          </ProtectedRoute>
        </InitCheckRoute>
      ),
      children: [
        {
          index: true,
          element: <HomeRoute />,
        },
        {
          path: 'dashboard',
          element: <HomeRoute />,
        },
        {
          path: 'settings',
          element: <SettingsPage />,
        },
        {
          path: 'crds/:crd',
          element: <CRListPage />,
        },
        {
          path: 'charts',
          element: <HelmChartListPage />,
        },
        {
          path: 'charts/:repository/:name',
          element: <HelmChartDetailPage />,
        },
        {
          path: 'crds/:resource/:namespace/:name',
          element: <ResourceDetail />,
        },
        {
          path: 'crds/:resource/:name',
          element: <ResourceDetail />,
        },
        // Platform module routes (application stays ahead of the :resource
        // wildcards below, design §11); each block is imported from its own
        // module directory.
        ...applicationRoutes,
        ...buildfleetRoutes,
        ...workflowRoutes,
        {
          path: ':resource/:name',
          element: <ResourceDetail />,
        },
        {
          path: ':resource',
          element: <ResourceList />,
        },
        {
          path: ':resource/:namespace/:name',
          element: <ResourceDetail />,
        },
      ],
    },
  ],
  {
    basename: subPath,
  }
)
