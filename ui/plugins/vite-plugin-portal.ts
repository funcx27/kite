import { existsSync, readFileSync, writeFileSync } from 'fs'
import path from 'path'
import type { Plugin } from 'vite'

// Platform-owned vite extensions (portal entry, dev fallback, shared chunks),
// kept here so vite.config.ts stays close to upstream.

const runtimeBasePlaceholder = '__KITE_BASE__'

// Path prefix owned by the portal SPA entry. Must match the server-side
// fallback rule in static.go.
const portalRoutePrefix = '/portal'

// The dev server's SPA fallback only knows about index.html; without this,
// hard navigations to portal routes would boot the console entry, whose
// :resource catch-all would render them as cluster resource pages.
export function portalSpaFallbackPlugin(devSubPath: string): Plugin {
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

// portal.html is served at arbitrary SPA routes just like index.html, so its
// asset URLs need the same runtime-base rewrite as runtimeBaseHtmlPlugin in
// vite.config.ts (which handles index.html).
export function portalRuntimeBaseHtmlPlugin(): Plugin {
  let buildOutDir = ''

  return {
    name: 'kite-portal-runtime-base-html',
    apply: 'build',
    configResolved(config) {
      buildOutDir = path.resolve(config.root, config.build.outDir)
    },
    closeBundle() {
      const htmlPath = path.join(buildOutDir, 'portal.html')
      if (!existsSync(htmlPath)) return
      const html = readFileSync(htmlPath, 'utf8')

      const nextHtml = html.replaceAll(
        /((?:href|src)=["'])\.\/assets\//g,
        `$1${runtimeBasePlaceholder}/assets/`
      )

      if (nextHtml !== html) {
        writeFileSync(htmlPath, nextHtml)
      }
    },
  }
}

// Both HTML entries as build inputs; the console entry keeps the default
// `main` slot.
export function portalBuildInput() {
  return {
    main: path.resolve(import.meta.dirname, '../index.html'),
    portal: path.resolve(import.meta.dirname, '../portal.html'),
  }
}

// Chunk naming shared by both entries: the vendor rule keeps one cacheable
// chunk instead of per-entry copies, and the portal component copies must
// land in the same chunks as the console originals (base matcher).
export function withPortalChunks(
  base: (id: string) => string | undefined
): (id: string) => string | undefined {
  return (id) => {
    const normalizedId = id.replaceAll('\\', '/')

    if (
      normalizedId.includes('/node_modules/react/') ||
      normalizedId.includes('/node_modules/react-dom/') ||
      normalizedId.includes('/node_modules/scheduler/') ||
      normalizedId.includes('/node_modules/@tanstack/')
    ) {
      return 'vendor'
    }

    if (normalizedId.includes('/src/portal/components/terminal-content.tsx')) {
      return 'terminal'
    }
    if (
      normalizedId.includes('/src/portal/components/log-viewer-content.tsx')
    ) {
      return 'log-viewer'
    }

    return base(id)
  }
}
