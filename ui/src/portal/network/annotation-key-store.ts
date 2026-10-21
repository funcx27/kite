import { useSyncExternalStore } from 'react'

import { mockAnnotationKeys } from './mock'
import { NetworkAnnotationKey } from './types'

let keys = mockAnnotationKeys
const listeners = new Set<() => void>()

function emit() {
  for (const listener of listeners) listener()
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

// Frontend mock of the platform catalog API: shared mutable state so edits
// on the settings sub-page show up in the host annotation dialog (and vice
// versa) without a real backend round trip.
export function useAnnotationKeys(): NetworkAnnotationKey[] {
  return useSyncExternalStore(subscribe, () => keys)
}

export function addAnnotationKey(entry: NetworkAnnotationKey): boolean {
  if (keys.some((existing) => existing.key === entry.key)) return false
  keys = [...keys, entry]
  emit()
  return true
}

export function importAnnotationKeys(entries: NetworkAnnotationKey[]): {
  imported: number
  skipped: number
} {
  const existing = new Set(keys.map((k) => k.key))
  const fresh = entries.filter((entry) => !existing.has(entry.key))
  if (fresh.length > 0) {
    keys = [...keys, ...fresh]
    emit()
  }
  return { imported: fresh.length, skipped: entries.length - fresh.length }
}

export function updateAnnotationKey(
  key: string,
  changes: { description: string; ingressClass: string }
) {
  keys = keys.map((k) => (k.key === key ? { ...k, ...changes } : k))
  emit()
}

export function removeAnnotationKey(key: string) {
  keys = keys.filter((k) => k.key !== key)
  emit()
}
