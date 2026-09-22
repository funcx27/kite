import { useCallback, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import {
  IconClearAll,
  IconMaximize,
  IconMinimize,
  IconPalette,
  IconX,
} from '@tabler/icons-react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal as XTerm } from '@xterm/xterm'

import '@xterm/xterm/css/xterm.css'

import { useTranslation } from 'react-i18next'

import { TERMINAL_THEMES, TerminalTheme } from '@/types/themes'
import { getWebSocketUrl } from '@/lib/subpath'
import { ConnectionIndicator } from '@/components/connection-indicator'

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

function applyTheme(term: XTerm, theme: TerminalTheme) {
  const t = TERMINAL_THEMES[theme]
  term.options.theme = {
    background: t.background,
    foreground: t.foreground,
    cursor: t.cursor,
    selectionBackground: t.selection,
  }
}

export function KcrossPeerTerminal({
  peerId,
  gateway,
  onClose,
}: {
  peerId: string
  gateway: string
  onClose: () => void
}) {
  const { t } = useTranslation()
  const termRef = useRef<HTMLDivElement>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  const xtermRef = useRef<XTerm | null>(null)
  const [connected, setConnected] = useState(false)
  const [fullscreen, setFullscreen] = useState(false)
  const [sessionKey, setSessionKey] = useState(0)
  const [terminalTheme, setTerminalTheme] = useState<TerminalTheme>(() => {
    return (localStorage.getItem('terminal-theme') as TerminalTheme) || 'classic'
  })

  // 主题循环切换，与 pod 终端共用同一份持久化主题
  const cycleTheme = useCallback(() => {
    setTerminalTheme((current) => {
      const themes = Object.keys(TERMINAL_THEMES) as TerminalTheme[]
      const next = themes[(themes.indexOf(current) + 1) % themes.length]
      localStorage.setItem('terminal-theme', next)
      return next
    })
  }, [])

  // 主题变更只改 xterm options，不重建会话
  useEffect(() => {
    if (xtermRef.current) {
      applyTheme(xtermRef.current, terminalTheme)
    }
  }, [terminalTheme])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  useEffect(() => {
    const term = new XTerm({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: '"Maple Mono", Monaco, Menlo, "Ubuntu Mono", monospace',
    })
    applyTheme(term, terminalTheme)
    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    xtermRef.current = term
    if (termRef.current) {
      term.open(termRef.current)
      fitAddon.fit()
    }

    const ws = new WebSocket(
      getWebSocketUrl(
        `/api/v1/admin/kcross/peers/${peerId}/pty?gateway=${encodeURIComponent(gateway)}&cols=${term.cols}&rows=${term.rows}`
      )
    )
    ws.binaryType = 'arraybuffer'

    const decoder = new TextDecoder()
    const encoder = new TextEncoder()

    ws.onopen = () => {
      setConnected(true)
      term.focus()
      fitAddon.fit()
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
          const code =
            payload.length >= 4 ? new DataView(payload.buffer).getInt32(0, true) : -1
          term.write(
            `\r\n\x1b[33m${t('kcrossManagement.terminalExited', 'Session exited ({{code}})', { code })}\x1b[0m\r\n`
          )
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
      term.write(
        `\r\n\x1b[33m${t('kcrossManagement.terminalClosed', 'Session closed')}\x1b[0m\r\n`
      )
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
    if (containerRef.current) {
      observer.observe(containerRef.current)
    }

    return () => {
      observer.disconnect()
      onData.dispose()
      onResize.dispose()
      ws.close()
      term.dispose()
      xtermRef.current = null
    }
  }, [peerId, gateway, sessionKey, t])

  const clearTerminal = useCallback(() => {
    xtermRef.current?.clear()
    xtermRef.current?.focus()
  }, [])

  // 始终 portal 到 body 的固定层：DOM 树位置恒定，xterm 不会因
  // 尺寸/全屏切换被重建；默认接近全屏，全屏按钮仅切换几何类名
  return createPortal(
    <div
      ref={containerRef}
      className={
        fullscreen
          ? 'fixed inset-0 z-[100] flex flex-col gap-2 bg-background p-3'
          : 'fixed inset-4 z-[100] flex flex-col gap-2 rounded-lg border bg-background p-3 shadow-lg'
      }
    >
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <span className="text-sm font-medium">
            {t('kcrossManagement.terminalTitle', 'Terminal — {{peer}}', { peer: peerId })}
          </span>
          <ConnectionIndicator
            isConnected={connected}
            onReconnect={connected ? undefined : () => setSessionKey((k) => k + 1)}
          />
        </div>
        <div className="flex items-center gap-1">
          <button
            type="button"
            title={t('kcrossManagement.cycleTheme', 'Cycle theme')}
            onClick={cycleTheme}
            className="rounded p-1.5 hover:bg-muted"
          >
            <IconPalette className="h-4 w-4" />
          </button>
          <button
            type="button"
            title={t('kcrossManagement.clearTerminal', 'Clear')}
            onClick={clearTerminal}
            className="rounded p-1.5 hover:bg-muted"
          >
            <IconClearAll className="h-4 w-4" />
          </button>
          <button
            type="button"
            title={
              fullscreen
                ? t('common.actions.exitFullscreen', 'Exit fullscreen')
                : t('common.actions.fullscreen', 'Fullscreen')
            }
            onClick={() => setFullscreen((v) => !v)}
            className="rounded p-1.5 hover:bg-muted"
          >
            {fullscreen ? (
              <IconMinimize className="h-4 w-4" />
            ) : (
              <IconMaximize className="h-4 w-4" />
            )}
          </button>
          <button
            type="button"
            title={t('common.actions.close', 'Close')}
            onClick={onClose}
            className="rounded p-1.5 hover:bg-muted"
          >
            <IconX className="h-4 w-4" />
          </button>
        </div>
      </div>
      <div ref={termRef} className="min-h-0 flex-1 overflow-hidden rounded-md border bg-[#1c1c1c] p-1" />
    </div>,
    document.body
  )
}
