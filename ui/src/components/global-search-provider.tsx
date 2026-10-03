/* eslint-disable react-refresh/only-export-components */
import {
  createContext,
  ReactNode,
  useCallback,
  useContext,
  useEffect,
  useState,
} from 'react'
import { useWorkMode } from '@/platform/work-mode-context'

interface GlobalSearchContextType {
  isOpen: boolean
  openSearch: () => void
  closeSearch: () => void
  toggleSearch: () => void
}

const GlobalSearchContext = createContext<GlobalSearchContextType | undefined>(
  undefined
)

export function useGlobalSearch() {
  const context = useContext(GlobalSearchContext)
  if (context === undefined) {
    throw new Error(
      'useGlobalSearch must be used within a GlobalSearchProvider'
    )
  }
  return context
}

interface GlobalSearchProviderProps {
  children: ReactNode
}

export function GlobalSearchProvider({ children }: GlobalSearchProviderProps) {
  const [isOpen, setIsOpen] = useState(false)
  const { isApplicationMode } = useWorkMode()

  const openSearch = () => setIsOpen(true)
  const closeSearch = () => setIsOpen(false)
  const toggleSearch = useCallback(() => setIsOpen((prev) => !prev), [])

  useEffect(() => {
    // Global search is a professional-console affordance; in application mode
    // the shortcut must stay inert.
    if (isApplicationMode) return

    const handleKeyDown = (e: KeyboardEvent) => {
      // Command+K or Ctrl+K to open search
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault()
        toggleSearch()
      }

      // Escape to close search
      if (e.key === 'Escape' && isOpen) {
        closeSearch()
      }
    }

    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [isOpen, toggleSearch, isApplicationMode])

  const value = {
    isOpen,
    openSearch,
    closeSearch,
    toggleSearch,
  }

  return (
    <GlobalSearchContext.Provider value={value}>
      {children}
    </GlobalSearchContext.Provider>
  )
}
