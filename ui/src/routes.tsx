import { createBrowserRouter } from 'react-router-dom'

import App, { StandaloneAIChatApp } from './App'
import { ApplicationModeRoute, HomeRoute } from './components/home-route'
import { InitCheckRoute } from './components/init-check-route'
import { ProtectedRoute } from './components/protected-route'
import { getSubPath } from './lib/subpath'
import { ApplicationDetailPage } from './pages/application-detail-page'
import { ApplicationListPage } from './pages/application-list-page'
import { ApplicationPodPage } from './pages/application-pod-page'
import { BuildFleetPage } from './pages/buildfleet-page'
import { CRListPage } from './pages/cr-list-page'
import { HelmChartDetailPage } from './pages/helm-chart-detail-page'
import { HelmChartListPage } from './pages/helm-chart-list-page'
import { InitializationPage } from './pages/initialization'
import { LoginPage } from './pages/login'
import { ResourceDetail } from './pages/resource-detail'
import { ResourceList } from './pages/resource-list'
import { SettingsPage } from './pages/settings'

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
        // Application module routes stay ahead of the :resource wildcards
        // below (design §11) and are application-mode only: the professional
        // console does not cross-link them, so direct URLs bounce home.
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
        {
          path: 'buildfleet',
          element: <BuildFleetPage />,
        },
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
