// Mock in-memory notification store: the header bell and the inbox page
// read/write the same module-level instance. Snapshots are immutable — every
// mutation replaces the array reference — so useSyncExternalStore re-renders
// consumers. The real implementation swaps this module for the API +
// react-query.
import { useSyncExternalStore } from 'react'

import { mockNotifications } from './mock'
import type { NotificationItem } from './mock'

let notifications: NotificationItem[] = mockNotifications

const listeners = new Set<() => void>()

function emit() {
  for (const listener of listeners) listener()
}

export function getSnapshot(): NotificationItem[] {
  return notifications
}

export function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function markRead(id: string) {
  notifications = notifications.map((item) =>
    item.id === id ? { ...item, read: true } : item
  )
  emit()
}

export function markAllRead() {
  notifications = notifications.map((item) => ({ ...item, read: true }))
  emit()
}

export function useNotifications(): NotificationItem[] {
  return useSyncExternalStore(subscribe, getSnapshot)
}
