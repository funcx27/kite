// Run-view live stream hook: EventSource subscription on GET /runs/:id/events
// with reconnect handling (kept component-free for fast refresh; LogPane
// lives in components/log-pane.tsx).
import { useEffect, useRef, useState } from 'react'

import { runEventsUrl } from '../api'
import type { NodeRunStatus, RunStatus } from '../types'

export interface RunStreamState {
  // Overlays on top of the run detail snapshot; undefined until an event
  // arrives so the page can fall back to fetched state.
  runStatus?: RunStatus
  nodeStatuses: Record<string, NodeRunStatus>
  // Append-only node log lines keyed by nodeId (logTail + live log events).
  logs: Record<string, string[]>
  // Recent stream events for the run-level event feed (newest last).
  events: string[]
  connected: boolean
  ended: boolean
}

const MAX_EVENTS = 50

// useRunEventStream subscribes for the given run and stops itself after the
// terminal `done` event. EventSource retries the connection automatically on
// transport errors; a named `error` frame (server-sent) is surfaced as data.
// restartKey lets the caller re-open the stream after a done-then-restarted
// run (retry/cancel): changing it closes the old EventSource, drops the
// terminal overlay and re-aligns from the server snapshot.
export function useRunEventStream(
  runId: number | undefined,
  onDone?: () => void,
  restartKey: number = 0
): RunStreamState {
  const [state, setState] = useState<RunStreamState>({
    nodeStatuses: {},
    logs: {},
    events: [],
    connected: false,
    ended: false,
  })
  const onDoneRef = useRef(onDone)
  onDoneRef.current = onDone

  useEffect(() => {
    if (runId === undefined) return
    setState({
      nodeStatuses: {},
      logs: {},
      events: [],
      connected: false,
      ended: false,
    })
    const source = new EventSource(runEventsUrl(runId), {
      withCredentials: true,
    })

    const pushEvent = (text: string) =>
      setState((prev) => ({
        ...prev,
        events: [...prev.events.slice(-(MAX_EVENTS - 1)), text],
      }))

    const handleJson = <T>(event: MessageEvent): T | null => {
      try {
        return JSON.parse(event.data as string) as T
      } catch {
        return null
      }
    }

    source.addEventListener('open', () =>
      setState((prev) => ({ ...prev, connected: true }))
    )
    source.addEventListener('run', (event) => {
      const data = handleJson<{ status: RunStatus }>(event as MessageEvent)
      if (!data) return
      setState((prev) => ({ ...prev, runStatus: data.status }))
      pushEvent(`run → ${data.status}`)
    })
    source.addEventListener('node', (event) => {
      const data = handleJson<{ nodeId: string; status: NodeRunStatus }>(
        event as MessageEvent
      )
      if (!data) return
      setState((prev) => ({
        ...prev,
        nodeStatuses: { ...prev.nodeStatuses, [data.nodeId]: data.status },
      }))
      pushEvent(`node ${data.nodeId} → ${data.status}`)
    })
    source.addEventListener('log', (event) => {
      const data = handleJson<{ nodeId: string; data: string }>(
        event as MessageEvent
      )
      if (!data) return
      setState((prev) => {
        const lines = prev.logs[data.nodeId] ?? []
        return {
          ...prev,
          logs: {
            ...prev.logs,
            // log chunks may carry embedded newlines; keep them as lines.
            [data.nodeId]: [
              ...lines,
              ...(data.data ? data.data.split('\n') : ['']),
            ],
          },
        }
      })
    })
    source.addEventListener('done', (event) => {
      const data = handleJson<{ status: RunStatus }>(event as MessageEvent)
      setState((prev) => ({
        ...prev,
        runStatus: data?.status,
        connected: false,
        ended: true,
      }))
      source.close()
      onDoneRef.current?.()
    })
    // A server-sent `event: error` frame carries data; a transport failure
    // fires the native error without data and EventSource reconnects itself.
    source.addEventListener('error', (event) => {
      if ((event as MessageEvent).data === undefined) {
        setState((prev) => ({ ...prev, connected: false }))
        return
      }
      const data = handleJson<{ error: string }>(event as MessageEvent)
      setState((prev) => ({ ...prev, connected: false, ended: true }))
      if (data?.error) pushEvent(`error: ${data.error}`)
      source.close()
    })

    return () => source.close()
  }, [runId, restartKey])

  return state
}
