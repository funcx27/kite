import { useCallback } from 'react'
import { useSpaces, withSpaceParam } from '@/portal/space-context'
import {
  IconApps,
  IconChevronsLeft,
  IconChevronsRight,
  IconGitMerge,
  IconSettings,
} from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'
import { Link, useLocation } from 'react-router-dom'

import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from '@/components/ui/sidebar'

// Portal navigation: the workbench sidebar of the portal SPA, shown instead
// of the professional console's AppSidebar.
export function PortalSidebar(props: React.ComponentProps<typeof Sidebar>) {
  const { t } = useTranslation()
  const location = useLocation()
  const { currentSpace } = useSpaces()
  const { isMobile, setOpenMobile, state, toggleSidebar } = useSidebar()

  const handleMenuItemClick = useCallback(() => {
    if (isMobile) {
      setOpenMobile(false)
    }
  }, [isMobile, setOpenMobile])

  const isActive = useCallback(
    (path: string) => location.pathname === path,
    [location.pathname]
  )

  const collapsed = state === 'collapsed'

  return (
    <Sidebar collapsible="icon" {...props}>
      <SidebarContent>
        <SidebarGroup>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton
                tooltip={t('nav.applications')}
                asChild
                isActive={location.pathname.startsWith('/applications')}
                className="transition-all duration-200 hover:bg-accent/60 active:scale-95 data-[active=true]:bg-primary/10 data-[active=true]:text-primary data-[active=true]:shadow-sm"
              >
                <Link
                  to={withSpaceParam('/applications', currentSpace)}
                  onClick={handleMenuItemClick}
                >
                  <IconApps className="text-sidebar-primary" />
                  <span className="font-medium">{t('nav.applications')}</span>
                </Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
            <SidebarMenuItem>
              <SidebarMenuButton
                tooltip={t('workflow.title')}
                asChild
                isActive={location.pathname.startsWith('/workflows')}
                className="transition-all duration-200 hover:bg-accent/60 active:scale-95 data-[active=true]:bg-primary/10 data-[active=true]:text-primary data-[active=true]:shadow-sm"
              >
                <Link to="/workflows" onClick={handleMenuItemClick}>
                  <IconGitMerge className="text-sidebar-primary" />
                  <span className="font-medium">{t('workflow.title')}</span>
                </Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarGroup>
        <SidebarGroup>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton
                tooltip={t('applicationSettings.settings')}
                asChild
                isActive={isActive('/settings')}
                className="transition-all duration-200 hover:bg-accent/60 active:scale-95 data-[active=true]:bg-primary/10 data-[active=true]:text-primary data-[active=true]:shadow-sm"
              >
                <Link
                  to={withSpaceParam('/settings', currentSpace)}
                  onClick={handleMenuItemClick}
                >
                  <IconSettings className="text-sidebar-primary" />
                  <span className="font-medium">
                    {t('applicationSettings.settings')}
                  </span>
                </Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarGroup>
      </SidebarContent>
      <SidebarFooter>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              tooltip={collapsed ? t('sidebar.expand') : t('sidebar.collapse')}
              onClick={toggleSidebar}
              className="hidden md:flex"
            >
              {collapsed ? (
                <IconChevronsRight className="text-sidebar-primary" />
              ) : (
                <IconChevronsLeft className="text-sidebar-primary" />
              )}
              <span className="font-medium">
                {collapsed ? t('sidebar.expand') : t('sidebar.collapse')}
              </span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>
    </Sidebar>
  )
}
