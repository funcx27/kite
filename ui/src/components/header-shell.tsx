import type { ReactNode } from 'react'

import { Separator } from '@/components/ui/separator'

// Shared chrome of the mode top bars (SiteHeader, ApplicationHeader): the
// sticky bar shell, the right-aligned action cluster and the vertical
// divider, so both headers keep the same spacing and separator style.
export function HeaderShell({ children }: { children: ReactNode }) {
  return (
    <header className="sticky top-0 z-50 bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/60 flex h-(--header-height) shrink-0 items-center gap-2 border-b transition-[width,height] ease-linear group-has-data-[collapsible=icon]/sidebar-wrapper:h-(--header-height)">
      <div className="flex w-full items-center gap-1 px-4 lg:gap-2 lg:px-6">
        {children}
      </div>
    </header>
  )
}

export function HeaderActions({ children }: { children: ReactNode }) {
  return <div className="ml-auto flex items-center gap-2">{children}</div>
}

export function HeaderDivider() {
  return (
    <Separator
      orientation="vertical"
      className="mx-2 data-[orientation=vertical]:h-4"
    />
  )
}
