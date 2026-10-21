import { CSSProperties, ReactNode, StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { useTranslation } from 'react-i18next'
import {
  createBrowserRouter,
  Navigate,
  Outlet,
  RouterProvider,
} from 'react-router-dom'

// index.css must come first: its `@layer theme, base, components, utilities`
// statement fixes the cascade-layer order, and App.css's `@layer utilities`
// block would otherwise claim the first position and let base preflight
// override every spacing utility.
import '../index.css'
import '../App.css'
// Shared i18n first, then the portal shell's and each module's bundles; they
// all register into the same 'translation' namespace before first render.
import '../i18n'
import '@/portal/application/i18n'
import '@/portal/buildfleet/i18n'
import '@/portal/monitoring/i18n'
import '@/portal/workflow/i18n'
import './i18n'

import { applicationRoutes } from '@/portal/application/routes'
import { buildfleetRoutes } from '@/portal/buildfleet/routes'
import { monitoringRoutes } from '@/portal/monitoring/routes'
import { workflowRoutes } from '@/portal/workflow/routes'

import { AppearanceProvider } from '../components/appearance-provider'
import { ErrorBoundary } from '../components/error-boundary'
import { InitCheckRoute } from '../components/init-check-route'
import { ProtectedRoute } from '../components/protected-route'
import { SidebarInset, SidebarProvider } from '../components/ui/sidebar'
import { Toaster } from '../components/ui/sonner'
import { AuthProvider } from '../contexts/auth-context'
import { ClusterProvider } from '../contexts/cluster-context'
import { useCluster } from '../hooks/use-cluster'
import { QueryProvider } from '../lib/query-provider'
import { getSubPath } from '../lib/subpath'
import { InitializationPage } from '../pages/initialization'
import { LoginPage } from '../pages/login'
import { PortalHeader } from './portal-header'
import { PortalSidebar } from './portal-sidebar'
import { PlatformProviders } from './providers'

// Cluster list gate, mirroring App.tsx: the shell (space picker) and the
// module pages read cluster state, so hold rendering until it resolves.
function ClusterGate({ children }: { children: ReactNode }) {
  const { t } = useTranslation()
  const { isLoading, error } = useCluster()

  if (isLoading) {
    return (
      <div className="flex h-screen items-center justify-center">
        <div className="flex items-center space-x-2">
          <div className="h-4 w-4 animate-spin rounded-full border-2 border-gray-300 border-t-blue-600" />
          <span>{t('cluster.loading')}</span>
        </div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex h-screen items-center justify-center">
        <div className="text-red-500">
          <p>{t('cluster.error', { error: error.message })}</p>
        </div>
      </div>
    )
  }

  return <>{children}</>
}

function PlatformShell() {
  return (
    <>
      {/* Narrower than the console sidebar; the override must sit on the
          provider because the gap and container both read --sidebar-width. */}
      <SidebarProvider style={{ '--sidebar-width': '12rem' } as CSSProperties}>
        <PortalSidebar variant="inset" />
        <SidebarInset className="h-dvh overflow-y-auto overscroll-none scrollbar-hide">
          <PortalHeader />
          <div className="@container/main flex min-h-0 flex-1 flex-col">
            <div className="flex min-h-0 flex-1 flex-col gap-4 py-4 md:gap-6">
              <div className="flex min-h-0 flex-1 flex-col px-4 lg:px-6">
                <ErrorBoundary>
                  <Outlet />
                </ErrorBoundary>
              </div>
            </div>
          </div>
        </SidebarInset>
      </SidebarProvider>
      <Toaster />
    </>
  )
}

function PlatformApp() {
  return (
    <ClusterProvider>
      <PlatformProviders>
        <ClusterGate>
          <PlatformShell />
        </ClusterGate>
      </PlatformProviders>
    </ClusterProvider>
  )
}

const subPath = getSubPath()

// /login and /setup are mounted because the shared ProtectedRoute and
// InitCheckRoute redirect to them client-side; without local routes those
// redirects would render nothing.
const router = createBrowserRouter(
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
      path: '/',
      element: (
        <InitCheckRoute>
          <ProtectedRoute>
            <PlatformApp />
          </ProtectedRoute>
        </InitCheckRoute>
      ),
      children: [
        // The platform SPA has no landing page of its own; bare visits land
        // on the application list. The router basename adds the /portal
        // prefix, so route paths below stay identical to the console router's
        // while platform URLs live under /portal.
        {
          index: true,
          element: <Navigate to="/applications" replace />,
        },
        ...applicationRoutes,
        ...monitoringRoutes,
        ...workflowRoutes,
        ...buildfleetRoutes,
      ],
    },
  ],
  {
    basename: `${subPath}/portal`,
  }
)

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryProvider>
      <AppearanceProvider
        defaultTheme="system"
        defaultColorTheme="default"
        defaultFont="maple"
      >
        <AuthProvider>
          <RouterProvider router={router} />
        </AuthProvider>
      </AppearanceProvider>
    </QueryProvider>
  </StrictMode>
)
