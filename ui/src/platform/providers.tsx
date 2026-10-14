import type { ReactNode } from 'react'

import { SpaceProvider } from './space-context'

// The platform shell's context stack, mounted by the platform SPA root
// (src/platform/main.tsx) as the single shell-level integration point.
export function PlatformProviders({ children }: { children: ReactNode }) {
  return <SpaceProvider>{children}</SpaceProvider>
}
