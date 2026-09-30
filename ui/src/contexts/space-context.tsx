/* eslint-disable react-refresh/only-export-components */
import { createContext, useContext, useEffect, useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Namespace } from 'kubernetes-types/core/v1'

import { createResource, useResources } from '@/lib/api/core'
import { SPACE_LABEL, SPACE_LABEL_SELECTOR } from '@/lib/applications'

const CURRENT_SPACE_STORAGE_KEY = 'current-space'

interface SpaceContextType {
  // Names of the namespaces labeled kite.application/space=true.
  spaces: string[]
  // null means "all spaces".
  currentSpace: string | null
  setCurrentSpace: (space: string | null) => void
  // Creates the namespace on the given cluster and labels it as a space.
  createSpace: (name: string, cluster: string) => Promise<void>
  isLoading: boolean
  error: Error | null
}

const SpaceContext = createContext<SpaceContextType | undefined>(undefined)

function readStoredSpace(): string | null {
  const stored = localStorage.getItem(CURRENT_SPACE_STORAGE_KEY)
  return stored === '' || stored === null ? null : stored
}

export function SpaceProvider({ children }: { children: React.ReactNode }) {
  const queryClient = useQueryClient()
  const [currentSpace, setCurrentSpaceState] = useState<string | null>(() =>
    readStoredSpace()
  )
  const {
    data: namespaces,
    isLoading,
    error,
  } = useResources('namespaces', undefined, {
    labelSelector: SPACE_LABEL_SELECTOR,
    staleTime: 30 * 1000,
  })

  const spaces = useMemo(() => {
    return (namespaces ?? [])
      .map((ns: Namespace) => ns.metadata?.name || '')
      .filter((name: string) => name !== '')
      .sort()
  }, [namespaces])

  useEffect(() => {
    if (currentSpace === null) {
      localStorage.removeItem(CURRENT_SPACE_STORAGE_KEY)
      return
    }
    localStorage.setItem(CURRENT_SPACE_STORAGE_KEY, currentSpace)
  }, [currentSpace])

  // The persisted space must still be a labeled space, otherwise fall back
  // to "all spaces" (e.g. after a cluster switch).
  useEffect(() => {
    if (
      currentSpace !== null &&
      spaces.length > 0 &&
      !spaces.includes(currentSpace)
    ) {
      setCurrentSpaceState(null)
    }
  }, [spaces, currentSpace])

  const setCurrentSpace = (space: string | null) => {
    setCurrentSpaceState(space)
  }

  const createSpace = async (name: string, cluster: string) => {
    await createResource(
      'namespaces',
      undefined,
      {
        apiVersion: 'v1',
        kind: 'Namespace',
        metadata: {
          name,
          labels: { [SPACE_LABEL]: 'true' },
        },
      },
      { cluster }
    )
    await queryClient.invalidateQueries({ queryKey: ['namespaces'] })
  }

  const value: SpaceContextType = {
    spaces,
    currentSpace,
    setCurrentSpace,
    createSpace,
    isLoading,
    error,
  }

  return <SpaceContext.Provider value={value}>{children}</SpaceContext.Provider>
}

export function useSpaces() {
  const context = useContext(SpaceContext)
  if (!context) {
    throw new Error('useSpaces must be used within a SpaceProvider')
  }
  return context
}
