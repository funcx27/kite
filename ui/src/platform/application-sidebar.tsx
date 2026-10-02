import { useCallback } from 'react'
import Icon from '@/assets/icon.svg'
import { IconApps } from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'
import { Link, useLocation } from 'react-router-dom'

import { useVersionInfo } from '@/lib/api'
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from '@/components/ui/sidebar'
import { VersionInfo } from '@/components/version-info'

// Application-mode navigation: the workbench sidebar shown instead of the
// professional console's AppSidebar while the work mode is "application".
export function ApplicationSidebar(
  props: React.ComponentProps<typeof Sidebar>
) {
  const { t } = useTranslation()
  const location = useLocation()
  const { isMobile, setOpenMobile } = useSidebar()

  const handleMenuItemClick = useCallback(() => {
    if (isMobile) {
      setOpenMobile(false)
    }
  }, [isMobile, setOpenMobile])

  const isActive = useCallback(
    (path: string) => location.pathname === path,
    [location.pathname]
  )

  return (
    <Sidebar collapsible="offcanvas" {...props}>
      <SidebarLogoHeader onClick={handleMenuItemClick} />
      <SidebarContent>
        <SidebarGroup>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton
                tooltip={t('nav.applications')}
                asChild
                isActive={isActive('/applications')}
                className="transition-all duration-200 hover:bg-accent/60 active:scale-95 data-[active=true]:bg-primary/10 data-[active=true]:text-primary data-[active=true]:shadow-sm"
              >
                <Link to="/applications" onClick={handleMenuItemClick}>
                  <IconApps className="text-sidebar-primary" />
                  <span className="font-medium">{t('nav.applications')}</span>
                </Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarGroup>
      </SidebarContent>
    </Sidebar>
  )
}

function SidebarLogoHeader({ onClick }: { onClick: () => void }) {
  const { t } = useTranslation()
  const { data: versionInfo } = useVersionInfo()

  return (
    <SidebarHeader>
      <SidebarMenu>
        <SidebarMenuItem>
          <SidebarMenuButton
            asChild
            className="data-[slot=sidebar-menu-button]:!p-1.5 hover:bg-accent/50 transition-colors"
          >
            <Link to="/" onClick={onClick}>
              <div className="relative flex items-center justify-between w-full">
                <div className="flex items-center gap-2">
                  <img src={Icon} alt="Kite Logo" className="h-8 w-8" />
                  <div className="flex flex-col">
                    <span className="text-base font-semibold bg-gradient-to-r from-primary to-primary/70 bg-clip-text text-transparent">
                      Kite
                    </span>
                    <VersionInfo />
                  </div>
                </div>
                {versionInfo?.hasNewVersion ? (
                  <button
                    type="button"
                    onClick={(e) => {
                      e.preventDefault()
                      e.stopPropagation()
                      if (versionInfo?.releaseUrl) {
                        window.open(versionInfo.releaseUrl, '_blank')
                      }
                    }}
                    className="absolute right-0 top-0 mr-1 mt-1 rounded-sm bg-red-500/10 px-1.5 py-0.5 text-[9px] font-semibold uppercase text-red-500 hover:bg-red-500/20"
                    title={t('sidebar.updateAvailable')}
                  >
                    New
                  </button>
                ) : null}
              </div>
            </Link>
          </SidebarMenuButton>
        </SidebarMenuItem>
      </SidebarMenu>
    </SidebarHeader>
  )
}
