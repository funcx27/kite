// Auto-scrolling monospace log view for one node's merged log text (module
// local: the shared log viewer is pod-oriented).
import { useEffect, useRef } from 'react'

export function LogPane({ text }: { text: string }) {
  const containerRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const el = containerRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [text])

  return (
    <div
      ref={containerRef}
      className="h-full min-h-0 overflow-auto rounded-md bg-zinc-950 p-3 font-mono text-xs leading-5 text-zinc-100"
    >
      {text ? (
        <pre className="whitespace-pre-wrap break-all">{text}</pre>
      ) : null}
    </div>
  )
}
