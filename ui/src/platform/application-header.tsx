import { SpacePicker } from '@/application/components/space-picker'

import { useIsMobile } from '@/hooks/use-mobile'
import { SidebarTrigger } from '@/components/ui/sidebar'
import { LanguageToggle } from '@/components/language-toggle'
import { ModeToggle } from '@/components/mode-toggle'

import { HeaderActions, HeaderDivider, HeaderShell } from './header-shell'
import { UserMenu } from './user-menu'

// Application-mode top bar: the space picker is global here so the current
// space stays visible and switchable on every application page. On mobile the
// application sidebar is a drawer, so the trigger is the only way to open it;
// on desktop the collapse toggle lives in the sidebar footer.
export function ApplicationHeader() {
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
