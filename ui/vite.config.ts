import { existsSync, readFileSync, writeFileSync } from 'fs'
import path from 'path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig, type Plugin } from 'vite'

import monacoEditorFeatures from './plugins/vite-plugin-monaco-editor.ts'
import { normalizeBasePath } from './src/lib/base-path.ts'

const devSubPath = normalizeBasePath(process.env.KITE_BASE)
const runtimeBasePlaceholder = '__KITE_BASE__'

function getDevBase() {
  return devSubPath ? `${devSubPath}/` : '/'
}

function getManualChunk(id: string) {
  const normalizedId = id.replaceAll('\\', '/')

  // Shared across both HTML entries; a stable name keeps it one cacheable
  // chunk instead of per-entry copies.
  if (
    normalizedId.includes('/node_modules/react/') ||
    normalizedId.includes('/node_modules/react-dom/') ||
    normalizedId.includes('/node_modules/scheduler/') ||
    normalizedId.includes('/node_modules/@tanstack/')
  ) {
    return 'vendor'
  }

  if (
    normalizedId.includes('/src/lib/monaco-runtime.ts') ||
    normalizedId.includes('/node_modules/@monaco-editor/') ||
    normalizedId.includes('/node_modules/monaco-editor/')
  ) {
    return 'monaco'
  }

  // Console and portal copies must land in the same cacheable chunk.
  if (
    normalizedId.includes('/src/components/terminal-content.tsx') ||
    normalizedId.includes('/src/portal/components/terminal-content.tsx') ||
    normalizedId.includes('/node_modules/@xterm/')
  ) {
    return 'terminal'
  }

  // Console and portal copies must land in the same cacheable chunk.
  if (
    normalizedId.includes('/src/components/log-viewer-content.tsx') ||
    normalizedId.includes('/src/portal/components/log-viewer-content.tsx') ||
    normalizedId.includes('/src/lib/ansi-parser.ts')
  ) {
    return 'log-viewer'
  }

  if (
    normalizedId.includes('/node_modules/recharts/') ||
    normalizedId.includes('/node_modules/victory-vendor/')
  ) {
    return 'recharts'
  }

  return undefined
}

// Path prefix owned by the portal SPA entry. Must match the server-side
// fallback rule in static.go.
const portalRoutePrefix = '/portal'

// The dev server's SPA fallback only knows about index.html; without this,
// hard navigations to portal routes would boot the console entry, whose
// :resource catch-all would render them as cluster resource pages.
function portalSpaFallbackPlugin(): Plugin {
  return {
    name: 'kite-portal-spa-fallback',
    apply: 'serve',
    configureServer(server) {
      server.middlewares.use((req, _res, next) => {
        if (req.method !== 'GET' && req.method !== 'HEAD') return next()
        if (!req.headers.accept?.includes('text/html')) return next()

        const pathname = req.url?.split('?', 1)[0] ?? ''
        const withoutBase = devSubPath
          ? pathname.slice(devSubPath.length)
          : pathname
        if (
          withoutBase === portalRoutePrefix ||
          withoutBase.startsWith(`${portalRoutePrefix}/`)
        ) {
          req.url = `${devSubPath}/portal.html`
        }
        next()
      })
    },
  }
}

function runtimeBaseHtmlPlugin(): Plugin {
  let buildOutDir = ''

  return {
    name: 'kite-runtime-base-html',
    apply: 'build',
    configResolved(config) {
      buildOutDir = path.resolve(config.root, config.build.outDir)
    },
    closeBundle() {
      // Every HTML entry served at arbitrary SPA routes needs the runtime
      // base in its asset URLs; relative ./assets would resolve against the
      // current route instead of the deploy root.
      for (const htmlName of ['index.html', 'portal.html']) {
        const htmlPath = path.join(buildOutDir, htmlName)
        if (!existsSync(htmlPath)) continue
        const html = readFileSync(htmlPath, 'utf8')

        // Make the first HTML-loaded assets runtime-base aware without relying on <base href>.
        const nextHtml = html.replaceAll(
          /((?:href|src)=["'])\.\/assets\//g,
          `$1${runtimeBasePlaceholder}/assets/`
        )

        if (nextHtml !== html) {
          writeFileSync(htmlPath, nextHtml)
        }
      }
    },
  }
}

export default defineConfig(({ command }) => ({
  base: command === 'build' ? './' : getDevBase(),
  plugins: [
    react(),
    tailwindcss(),
    monacoEditorFeatures({
      features: [
        'bracketMatching',
        'clipboard',
        'comment',
        'cursorUndo',
        'find',
        'folding',
        'indentation',
        'lineSelection',
        'linesOperations',
        'readOnlyMessage',
        'tokenization',
        'toggleTabFocusMode',
        'wordOperations',
      ],
    }),
    runtimeBaseHtmlPlugin(),
    portalSpaFallbackPlugin(),
  ],
  envPrefix: ['VITE_', 'KITE_'],
  build: {
    outDir: '../static',
    emptyOutDir: true,
    chunkSizeWarningLimit: 3000,
    modulePreload: true,
    rolldownOptions: {
      input: {
        main: path.resolve(import.meta.dirname, 'index.html'),
        portal: path.resolve(import.meta.dirname, 'portal.html'),
      },
      output: {
        manualChunks: getManualChunk,
      },
    },
  },
  server: {
    watch: {
      ignored: ['**/.vscode/**'],
      // Polling on this setup (WSL2 + dedicated ext4 mount): inotify events
      // can fire before the write flushes, and the watcher then reads and
      // caches an empty file — the module silently exports nothing until the
      // dev server restarts.
      usePolling: true,
      interval: 300,
    },
    proxy: {
      [devSubPath + '/api/']: {
        changeOrigin: true,
        target: 'http://localhost:8080',
      },
      '^/ws/.*': {
        target: 'ws://localhost:8080',
        ws: true,
        rewriteWsOrigin: true,
      },
    },
  },
  resolve: {
    alias: {
      '@': path.resolve(import.meta.dirname, './src'),
    },
  },
  worker: {
    format: 'es',
  },
  define: {
    global: 'globalThis',
  },
}))
