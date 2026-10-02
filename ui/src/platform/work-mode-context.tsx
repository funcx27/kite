/* eslint-disable react-refresh/only-export-components */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
} from 'react'
import type { WorkMode } from '@/application/lib'
import { useAuth } from '@/contexts/auth-context'

const WORK_MODE_STORAGE_KEY = 'work-mode'

interface WorkModeContextType {
  mode: WorkMode
  isApplicationMode: boolean
  setMode: (mode: WorkMode) => void
}

const WorkModeContext = createContext<WorkModeContextType | undefined>(
  undefined
)

function readStoredMode(): WorkMode | null {
  const stored = localStorage.getItem(WORK_MODE_STORAGE_KEY)
  return stored === 'application' || stored === 'professional' ? stored : null
}

// Role-based default until the user picks a mode: viewers work in the
// application mode, everyone else sees the full k8s console.
function defaultModeForRoles(roles?: { name: string }[]): WorkMode {
  return roles?.some((role) => role.name === 'viewer')
    ? 'application'
    : 'professional'
}

export function WorkModeProvider({ children }: { children: React.ReactNode }) {
  const { user } = useAuth()
  const [mode, setModeState] = useState<WorkMode>(
    () => readStoredMode() ?? 'professional'
  )

  useEffect(() => {
    if (!readStoredMode()) {
      setModeState(defaultModeForRoles(user?.roles))
    }
  }, [user])

  const setMode = useCallback((next: WorkMode) => {
    setModeState(next)
    localStorage.setItem(WORK_MODE_STORAGE_KEY, next)
  }, [])

  const value: WorkModeContextType = {
    mode,
    isApplicationMode: mode === 'application',
    setMode,
  }

  return (
    <WorkModeContext.Provider value={value}>
      {children}
    </WorkModeContext.Provider>
  )
}

export function useWorkMode() {
  const context = useContext(WorkModeContext)
  if (!context) {
    throw new Error('useWorkMode must be used within a WorkModeProvider')
  }
  return context
}
