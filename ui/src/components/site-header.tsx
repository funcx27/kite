import { useState } from 'react'
import { useAuth } from '@/contexts/auth-context'
import { useTerminal } from '@/contexts/terminal-context'
import { Plus, Settings, TerminalSquare } from 'lucide-react'
import { useNavigate } from 'react-router-dom'

import { useIsMobile } from '@/hooks/use-mobile'
import { Button } from '@/components/ui/button'
import { SidebarTrigger } from '@/components/ui/sidebar'

import { CreateResourceDialog } from './create-resource-dialog'
import { DynamicBreadcrumb } from './dynamic-breadcrumb'
import { HeaderActions, HeaderDivider, HeaderShell } from './header-shell'
import { LanguageToggle } from './language-toggle'
import { ModeToggle } from './mode-toggle'
import { Search } from './search'
import { UserMenu } from './user-menu'

export function SiteHeader() {
  const isMobile = useIsMobile()
  const navigate = useNavigate()
  const { user, capabilities } = useAuth()
  const { toggleTerminal, isOpen } = useTerminal()
  const [createDialogOpen, setCreateDialogOpen] = useState(false)
  const isAdmin = user?.isAdmin() ?? false
  const kubectlEnabled = capabilities.kubectlEnabled

  return (
    <>
      <HeaderShell>
        <SidebarTrigger className="-ml-1" />
        <HeaderDivider />
        <DynamicBreadcrumb />
        <HeaderActions>
          <Search />
          <Plus
            className="h-5 w-5 cursor-pointer text-muted-foreground hover:text-foreground"
            onClick={() => setCreateDialogOpen(true)}
            aria-label="Create new resource"
          />
          {isAdmin && kubectlEnabled && (
            <button
              onClick={toggleTerminal}
              title="Kubectl Terminal"
              aria-label="Toggle Kubectl Terminal"
              className={`flex items-center justify-center rounded-sm p-1 transition-colors ${
                isOpen
                  ? 'text-green-500 hover:text-green-600'
                  : 'text-muted-foreground hover:text-foreground'
              }`}
            >
              <TerminalSquare className="h-5 w-5" />
            </button>
          )}
          {!isMobile && (
            <>
              <HeaderDivider />
              {isAdmin && (
                <Button
                  variant="ghost"
                  size="icon"
                  onClick={() => navigate('/settings')}
                  className="hidden sm:flex"
                >
                  <Settings className="h-5 w-5" />
                  <span className="sr-only">Settings</span>
                </Button>
              )}
              <LanguageToggle />
              <ModeToggle />
            </>
          )}
          <UserMenu />
        </HeaderActions>
      </HeaderShell>

      {createDialogOpen ? (
        <CreateResourceDialog
          open={createDialogOpen}
          onOpenChange={setCreateDialogOpen}
        />
      ) : null}
    </>
  )
}
