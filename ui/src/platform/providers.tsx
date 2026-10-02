import type { ReactNode } from 'react'

import { SpaceProvider } from './space-context'
import { WorkModeProvider } from './work-mode-context'

// The platform shell's context stack, mounted by the host App.tsx as the
// single shell-level integration point (see modules/README.md).
export function PlatformProviders({ children }: { children: ReactNode }) {
  return (
    <WorkModeProvider>
      <SpaceProvider>{children}</SpaceProvider>
    </WorkModeProvider>
  )
}
