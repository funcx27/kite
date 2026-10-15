import { SpacePicker } from '@/portal/application/components/space-picker'

import { useIsMobile } from '@/hooks/use-mobile'
import { SidebarTrigger } from '@/components/ui/sidebar'
import { LanguageToggle } from '@/components/language-toggle'
import { ModeToggle } from '@/components/mode-toggle'

import {
  HeaderActions,
  HeaderDivider,
  HeaderShell,
} from './components/header-shell'
import { UserMenu } from './user-menu'

// Portal top bar: the space picker is global here so the current space stays
// visible and switchable on every application page. On mobile the portal
// sidebar is a drawer, so the trigger is the only way to open it; on desktop
// the collapse toggle lives in the sidebar footer.
export function PortalHeader() {
  const isMobile = useIsMobile()

  return (
    <HeaderShell>
      {isMobile && (
        <>
          <SidebarTrigger className="-ml-1" />
          <HeaderDivider />
        </>
      )}
      <SpacePicker />
      <HeaderActions>
        {!isMobile && (
          <>
            <HeaderDivider />
            <LanguageToggle />
            <ModeToggle />
          </>
        )}
        <UserMenu />
      </HeaderActions>
    </HeaderShell>
  )
}
