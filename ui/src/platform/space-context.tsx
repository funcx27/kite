/* eslint-disable react-refresh/only-export-components */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react'
import { SPACE_LABEL, SPACE_LABEL_SELECTOR } from '@/application/lib'
import { useQueryClient } from '@tanstack/react-query'
import { Namespace } from 'kubernetes-types/core/v1'
import { useSearchParams } from 'react-router-dom'

import { createResource, useResources } from '@/lib/api/core'

const CURRENT_SPACE_STORAGE_KEY = 'current-space'

export interface SpaceCreationFailure {
  cluster: string
  error: unknown
}

// Successful clusters are never rolled back when others fail.
export interface SpaceCreationResult {
  succeeded: string[]
  failed: SpaceCreationFailure[]
}

interface SpaceContextType {
  // Names of the namespaces labeled kite.application/space=true.
  spaces: string[]
  // null means "all spaces".
  currentSpace: string | null
  // Syncs the selection into state, the ?space= query param and localStorage.
  setCurrentSpace: (space: string | null) => void
  // Creates the labeled namespace on every given cluster; never throws —
  // per-cluster failures come back in the result so callers can report
  // partial success.
  createSpace: (
    name: string,
    clusters: string[]
  ) => Promise<SpaceCreationResult>
  isLoading: boolean
  error: Error | null
}

const SpaceContext = createContext<SpaceContextType | undefined>(undefined)

function readStoredSpace(): string | null {
  const stored = localStorage.getItem(CURRENT_SPACE_STORAGE_KEY)
  return stored === '' || stored === null ? null : stored
}

// A non-empty ?space= wins over the stored value so space deep links work;
// an absent or empty param falls back to the remembered space.
function readInitialSpace(searchParams: URLSearchParams): string | null {
  return searchParams.get('space') || readStoredSpace()
}

export function SpaceProvider({ children }: { children: React.ReactNode }) {
  const queryClient = useQueryClient()
  const [searchParams, setSearchParams] = useSearchParams()
  const [currentSpace, setCurrentSpaceState] = useState<string | null>(() =>
    readInitialSpace(searchParams)
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

  // Writes state and the ?space= query param (preserving other params such as
  // tab); replace avoids one history entry per switch.
  const setCurrentSpace = useCallback(
    (space: string | null) => {
      setCurrentSpaceState(space)
      setSearchParams(
        (prev) => {
          const nextParams = new URLSearchParams(prev)
          if (space === null) {
            nextParams.delete('space')
          } else {
            nextParams.set('space', space)
          }
          return nextParams
        },
        { replace: true }
      )
    },
    [setSearchParams]
  )

  useEffect(() => {
    if (currentSpace === null) {
      localStorage.removeItem(CURRENT_SPACE_STORAGE_KEY)
      return
    }
    localStorage.setItem(CURRENT_SPACE_STORAGE_KEY, currentSpace)
  }, [currentSpace])

  // The persisted space must still be a labeled space, otherwise fall back
  // to "all spaces" (e.g. after a cluster switch); going through
  // setCurrentSpace clears the ?space= param too.
  useEffect(() => {
    if (
      currentSpace !== null &&
      spaces.length > 0 &&
      !spaces.includes(currentSpace)
    ) {
      setCurrentSpace(null)
    }
  }, [spaces, currentSpace, setCurrentSpace])

  // Mirror manual URL edits and external navigation into state. An absent or
  // empty param is not a signal, otherwise it would clobber the remembered
  // space on mount.
  useEffect(() => {
    const spaceParam = searchParams.get('space')
    if (!spaceParam || spaceParam === currentSpace) {
      return
    }
    setCurrentSpaceState(spaceParam)
  }, [searchParams, currentSpace])

  const createSpace = async (
    name: string,
    clusterNames: string[]
  ): Promise<SpaceCreationResult> => {
    const results = await Promise.allSettled(
      clusterNames.map((cluster) =>
        createResource(
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
      )
    )
    const succeeded: string[] = []
    const failed: SpaceCreationFailure[] = []
    results.forEach((result, index) => {
      if (result.status === 'fulfilled') {
        succeeded.push(clusterNames[index])
      } else {
        failed.push({ cluster: clusterNames[index], error: result.reason })
      }
    })
    await queryClient.invalidateQueries({ queryKey: ['namespaces'] })
    return { succeeded, failed }
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
