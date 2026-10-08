/* eslint-disable react-refresh/only-export-components */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import { SPACE_LABEL_SELECTOR } from '@/application/lib'
import { useQueries } from '@tanstack/react-query'
import { NamespaceList } from 'kubernetes-types/core/v1'
import { useSearchParams } from 'react-router-dom'

import { apiClient } from '@/lib/api-client'
import { clusterHeader } from '@/lib/current-cluster'
import { useCluster } from '@/hooks/use-cluster'

const CURRENT_SPACE_STORAGE_KEY = 'current-space'

interface SpaceContextType {
  // Names of the namespaces labeled kite.application/space=true, aggregated
  // across every healthy cluster: a space exists only on the clusters it was
  // created on, so a single-cluster list would hide it everywhere else.
  spaces: string[]
  // null means "all spaces".
  currentSpace: string | null
  // Syncs the selection into state, the ?space= query param and localStorage.
  setCurrentSpace: (space: string | null) => void
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
  const [searchParams, setSearchParams] = useSearchParams()
  const [currentSpace, setCurrentSpaceState] = useState<string | null>(() =>
    readInitialSpace(searchParams)
  )
  // The space most recently written by setCurrentSpace whose router
  // navigation may still be in flight. While set, the URL-mirror effect must
  // not read the stale ?space= param back into state; it clears once the URL
  // reflects the write.
  const pendingUrlSpaceRef = useRef<string | null | undefined>(undefined)
  const { clusters } = useCluster()
  const healthyClusters = useMemo(
    () => clusters.filter((candidate) => !candidate.error).map((c) => c.name),
    [clusters]
  )
  // One labeled-namespace list per healthy cluster, merged below; keys ride
  // the 'namespaces' prefix so the quota upsert's invalidation refreshes the
  // picker. A failing cluster just contributes no spaces.
  const listResults = useQueries({
    queries: healthyClusters.map((cluster) => ({
      queryKey: ['namespaces', 'spaces', cluster],
      queryFn: () =>
        apiClient.get<NamespaceList>(
          `/namespaces/_all?labelSelector=${encodeURIComponent(SPACE_LABEL_SELECTOR)}`,
          { headers: clusterHeader(cluster) }
        ),
      staleTime: 30 * 1000,
      placeholderData: (prev: NamespaceList | undefined) => prev,
      retry: false,
    })),
  })

  const spaces = useMemo(() => {
    const names = new Set<string>()
    for (const result of listResults) {
      for (const ns of result.data?.items ?? []) {
        if (ns.metadata?.name) names.add(ns.metadata.name)
      }
    }
    return [...names].sort()
    // listResults identity churns every render; the derived items array is
    // what actually matters.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [listResults.map((r) => r.dataUpdatedAt).join()])
  const isLoading = listResults.some((r) => r.isLoading)
  const error =
    listResults.length > 0 && listResults.every((r) => r.isError)
      ? ((listResults[0].error as Error | null) ??
        new Error('failed to list spaces'))
      : null

  // Writes state and the ?space= query param (preserving other params such as
  // tab); replace avoids one history entry per switch.
  const setCurrentSpace = useCallback(
    (space: string | null) => {
      pendingUrlSpaceRef.current = space
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
  // space on mount. An in-flight own write is skipped until the URL catches
  // up, otherwise the stale param would revert the selection mid-navigation
  // (state commits before the router does).
  useEffect(() => {
    const spaceParam = searchParams.get('space')
    if (pendingUrlSpaceRef.current !== undefined) {
      if (spaceParam === pendingUrlSpaceRef.current) {
        pendingUrlSpaceRef.current = undefined
      }
      return
    }
    if (!spaceParam || spaceParam === currentSpace) {
      return
    }
    setCurrentSpaceState(spaceParam)
  }, [searchParams, currentSpace])

  const value: SpaceContextType = {
    spaces,
    currentSpace,
    setCurrentSpace,
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
