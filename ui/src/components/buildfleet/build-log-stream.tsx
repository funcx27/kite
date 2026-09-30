import { useCallback, useEffect, useRef, useState } from 'react'
import {
  IconCircle,
  IconCircleCheck,
  IconCircleX,
  IconLoader2,
} from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'

import {
  BuildRequest,
  BuildResultEvent,
  BuildVertexStatus,
} from '@/types/buildfleet'
import { stripAnsi } from '@/lib/ansi-parser'
import { startBuild } from '@/lib/api'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'

export interface BuildSession {
  request: BuildRequest
}

interface BuildLogStreamProps {
  // A new session object starts a new build stream; null returns to idle.
  session: BuildSession | null
  onRunningChange: (running: boolean) => void
  onResult: (result: BuildResultEvent) => void
  onError: (message: string) => void
}

interface VertexEntry {
  digest: string
  name: string
  status: BuildVertexStatus
  logs: string[]
}

const VERTEX_STATUS_META: Record<
  BuildVertexStatus,
  { icon: typeof IconCircle; className: string }
> = {
  pending: { icon: IconCircle, className: 'text-muted-foreground' },
  running: { icon: IconLoader2, className: 'text-blue-500' },
  complete: { icon: IconCircleCheck, className: 'text-green-600' },
  cached: { icon: IconCircleCheck, className: 'text-sky-500' },
  failed: { icon: IconCircleX, className: 'text-red-500' },
}

export function BuildLogStream({
  session,
  onRunningChange,
  onResult,
  onError,
}: BuildLogStreamProps) {
  const { t } = useTranslation()
  const [vertices, setVertices] = useState<VertexEntry[]>([])
  const [openOverrides, setOpenOverrides] = useState<
    Record<string, boolean | undefined>
  >({})
  const [streaming, setStreaming] = useState(false)

  const callbacksRef = useRef({ onRunningChange, onResult, onError })
  useEffect(() => {
    callbacksRef.current = { onRunningChange, onResult, onError }
  })

  const upsertVertex = useCallback(
    (digest: string, name: string, status: BuildVertexStatus) => {
      setVertices((prev) => {
        const index = prev.findIndex((v) => v.digest === digest)
        if (index === -1) {
          return [...prev, { digest, name, status, logs: [] }]
        }
        const next = [...prev]
        const current = next[index]
        next[index] = {
          ...current,
          name: current.name || name,
          status,
        }
        return next
      })
    },
    []
  )

  const appendLog = useCallback((vertex: string, data: string) => {
    setVertices((prev) => {
      const index = prev.findIndex((v) => v.digest === vertex)
      if (index === -1) {
        return [
          ...prev,
          { digest: vertex, name: '', status: 'running', logs: [data] },
        ]
      }
      const next = [...prev]
      next[index] = { ...next[index], logs: [...next[index].logs, data] }
      return next
    })
  }, [])

  useEffect(() => {
    if (!session) {
      setVertices([])
      setOpenOverrides({})
      setStreaming(false)
      callbacksRef.current.onRunningChange(false)
      return
    }

    const controller = new AbortController()
    let cancelled = false
    setVertices([])
    setOpenOverrides({})
    setStreaming(true)
    callbacksRef.current.onRunningChange(true)

    startBuild(
      session.request,
      {
        onVertex: (event) =>
          upsertVertex(event.digest, event.name, event.status),
        onLog: (event) => appendLog(event.vertex, event.data),
        onResult: (event) => {
          if (!cancelled) callbacksRef.current.onResult(event)
        },
        onError: (event) => {
          if (!cancelled) callbacksRef.current.onError(event.error)
        },
      },
      controller.signal
    )
      .catch((err: unknown) => {
        if (!cancelled && err instanceof Error && err.name !== 'AbortError') {
          callbacksRef.current.onError(err.message)
        }
      })
      .finally(() => {
        if (!cancelled) {
          setStreaming(false)
          callbacksRef.current.onRunningChange(false)
        }
      })

    return () => {
      cancelled = true
      // Aborting closes the connection; the adapter cancels the build when
      // the client disconnects.
      controller.abort()
    }
  }, [session, upsertVertex, appendLog])

  const isOpen = (vertex: VertexEntry) =>
    openOverrides[vertex.digest] ??
    (vertex.status === 'running' || vertex.status === 'failed')

  const finishedCount = vertices.filter(
    (v) => v.status !== 'pending' && v.status !== 'running'
  ).length

  return (
    <div className="flex flex-col gap-2">
      {vertices.length === 0 && (
        <p className="py-8 text-center text-sm text-muted-foreground">
          {streaming ? t('buildfleet.log.waiting') : t('buildfleet.log.idle')}
        </p>
      )}
      {vertices.length > 0 && (
        <p className="text-xs text-muted-foreground">
          {t('buildfleet.log.progress', {
            finished: finishedCount,
            total: vertices.length,
          })}
        </p>
      )}
      <div className="flex flex-col gap-1">
        {vertices.map((vertex) => {
          const meta = VERTEX_STATUS_META[vertex.status]
          const StatusIcon = meta.icon
          const text = stripAnsi(vertex.logs.join(''))
          return (
            <Collapsible
              key={vertex.digest}
              open={isOpen(vertex)}
              onOpenChange={(open) =>
                setOpenOverrides((prev) => ({
                  ...prev,
                  [vertex.digest]: open,
                }))
              }
            >
              <CollapsibleTrigger className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent/50">
                <StatusIcon
                  className={`size-4 shrink-0 ${meta.className} ${
                    vertex.status === 'running' ? 'animate-spin' : ''
                  }`}
                />
                <span className="truncate font-medium">
                  {vertex.name || vertex.digest.slice(7, 19)}
                </span>
                <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                  {t(`buildfleet.log.status.${vertex.status}`)}
                  {text && (
                    <>
                      {' · '}
                      {t('buildfleet.log.lines', {
                        lines: text.split('\n').length,
                      })}
                    </>
                  )}
                </span>
              </CollapsibleTrigger>
              <CollapsibleContent>
                {text && (
                  <pre className="mx-2 mb-2 max-h-64 overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted/50 p-2 font-mono text-xs">
                    {text}
                  </pre>
                )}
              </CollapsibleContent>
            </Collapsible>
          )
        })}
      </div>
    </div>
  )
}
