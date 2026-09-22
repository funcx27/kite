import { useEffect, useRef, useState } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal as XTerm } from '@xterm/xterm'

import '@xterm/xterm/css/xterm.css'

import { useTranslation } from 'react-i18next'

import { getWebSocketUrl } from '@/lib/subpath'

// kcross PTY 帧首字节 = pb.Channel 枚举
const CHANNEL_STDIN = 0
const CHANNEL_STDOUT = 1
const CHANNEL_STDERR = 2
const CHANNEL_RESIZE = 3
const CHANNEL_EXIT = 4
const CHANNEL_ERROR = 7

function frame(
  channel: number,
  payload: Uint8Array<ArrayBuffer>
): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(payload.length + 1)
  out[0] = channel
  out.set(payload, 1)
  return out
}

export function KcrossPeerTerminal({
  peerId,
  gateway,
  open,
}: {
  peerId: string
  gateway: string
  open: boolean
}) {
  const { t } = useTranslation()
  const termRef = useRef<HTMLDivElement>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const [connected, setConnected] = useState(false)

  useEffect(() => {
    if (!open) return

    const term = new XTerm({
      cursorBlink: true,
      fontSize: 13,
      theme: {
        background: '#1c1c1c',
      },
    })
    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    if (termRef.current) {
      term.open(termRef.current)
      fitAddon.fit()
    }

    const url = getWebSocketUrl(
      `/api/v1/admin/kcross/peers/${peerId}/pty?gateway=${encodeURIComponent(gateway)}&cols=${term.cols}&rows=${term.rows}`
    )
    const ws = new WebSocket(url)
    ws.binaryType = 'arraybuffer'
    wsRef.current = ws

    const decoder = new TextDecoder()
    const encoder = new TextEncoder()

    ws.onopen = () => {
      setConnected(true)
      term.focus()
    }
    ws.onmessage = (ev) => {
      if (!(ev.data instanceof ArrayBuffer)) return
      const data = new Uint8Array(ev.data)
      if (data.length < 1) return
      const channel = data[0]
      const payload = data.slice(1)
      switch (channel) {
        case CHANNEL_STDOUT:
        case CHANNEL_STDERR:
          term.write(decoder.decode(payload))
          break
        case CHANNEL_EXIT: {
          const code = payload.length >= 4 ? new DataView(payload.buffer).getInt32(0, true) : -1
          term.write(`\r\n\x1b[33m${t('kcrossManagement.terminalExited', 'Session exited ({{code}})', { code })}\x1b[0m\r\n`)
          ws.close()
          break
        }
        case CHANNEL_ERROR:
          term.write(`\r\n\x1b[31m${decoder.decode(payload)}\x1b[0m\r\n`)
          break
      }
    }
    ws.onclose = () => {
      setConnected(false)
      term.write(`\r\n\x1b[33m${t('kcrossManagement.terminalClosed', 'Session closed')}\x1b[0m\r\n`)
    }

    const onData = term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(frame(CHANNEL_STDIN, encoder.encode(data)))
      }
    })
    const resize = () => {
      fitAddon.fit()
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(
          frame(
            CHANNEL_RESIZE,
            encoder.encode(JSON.stringify({ rows: term.rows, cols: term.cols }))
          )
        )
      }
    }
    const onResize = term.onResize(resize)
    const observer = new ResizeObserver(resize)
    if (termRef.current) {
      observer.observe(termRef.current)
    }

    return () => {
      observer.disconnect()
      onData.dispose()
      onResize.dispose()
      ws.close()
      term.dispose()
      wsRef.current = null
    }
  }, [open, peerId, gateway, t])

  return (
    <div className="space-y-2">
      <div
        ref={termRef}
        className="h-96 overflow-hidden rounded-md border bg-[#1c1c1c] p-1"
      />
      <p className="text-xs text-muted-foreground">
        {connected
          ? t('kcrossManagement.terminalConnected', 'Connected to {{peer}}', { peer: peerId })
          : t('kcrossManagement.terminalConnecting', 'Connecting to {{peer}}…', { peer: peerId })}
      </p>
    </div>
  )
}
