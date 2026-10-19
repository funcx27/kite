import { useEffect, useRef, useState, type ReactNode } from 'react'
import * as yaml from 'js-yaml'
import { ChevronsLeft, ChevronsRight } from 'lucide-react'
import type { editor as monacoEditor } from 'monaco-editor'
import { useTranslation } from 'react-i18next'

import { MonacoDiffEditor, MonacoEditor } from '@/lib/monaco-loader'
import {
  defineMonacoBackgroundThemes,
  useMonacoBackgroundColor,
} from '@/lib/monaco-theme'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { useAppearance } from '@/components/appearance-provider'

// One in-diff side-pick widget: a ← → pair per changed path, floating over
// the center seam of the side-by-side diff (drift resolution). The drift
// call site renders the live (cluster) document as the original side, so ←
// picks the cluster value and → the DB value — each arrow points at the
// side it takes the value from.
export interface DiffLineWidget {
  path: string
  /** 1-based anchor line in the original (cluster) editor; 0 = no anchor. */
  originalLine: number
  /** 1-based anchor line in the modified (DB) editor; 0 = no anchor. */
  modifiedLine: number
  picked: 'db' | 'cluster'
  dbTitle: string
  clusterTitle: string
  onPick: (picked: 'db' | 'cluster') => void
}

interface YamlDiffViewerProps {
  /** Original YAML content (diff mode) */
  original?: string
  /** Modified YAML content (diff mode) */
  modified?: string
  /** Current YAML content (for current vs modified diff) */
  current?: string
  /** Whether the dialog is open */
  open: boolean
  /** Callback when dialog is closed */
  onOpenChange: (open: boolean) => void
  /** Callback when user wants to rollback to a specific version */
  onRollback?: (yamlContent: string) => void
  /** Whether rollback operation is in progress */
  isRollingBack?: boolean
  /** Dialog title */
  title?: string
  /** Height of the diff editor */
  height?: number
  /** Offer the diff-only view: collapsing unchanged regions is the initial
   *  state and a header toggle switches to the full YAML. When omitted the
   *  viewer always renders the full content (history compare behavior). */
  hideUnchanged?: boolean
  /** Extra header control (e.g. a confirm button for a rollback flow). */
  action?: ReactNode
  /** Skip the status-strip / re-dump pass. Required with lineWidgets: the
   *  re-dump would reflow lines and invalidate the widget line numbers. */
  raw?: boolean
  /** Side-pick arrow pairs floating over the diff's center seam, one row per
   *  changed path; also enables the top pick-all control. */
  lineWidgets?: DiffLineWidget[]
  /** Tooltip labels of the top pick-all control (all from cluster / all from
   *  DB); required for the control to render alongside lineWidgets. */
  pickAllLabels?: { cluster: string; db: string }
  /** Badges above the two panes naming each side (drift resolution: cluster
   *  live state vs DB config); omitted keeps the bare diff. */
  paneLabels?: { original: string; modified: string }
  /** Render a single read-only YAML editor with this document instead of the
   *  side-by-side diff (full-manifest view of one revision). */
  viewOnly?: boolean
  /** The document shown in viewOnly mode. */
  content?: string
}

type DiffMode = 'previous-vs-modified' | 'current-vs-modified'

export function YamlDiffViewer({
  original = '',
  modified = '',
  current,
  open,
  onOpenChange,
  onRollback,
  isRollingBack = false,
  title = 'YAML Diff',
  height = 600,
  hideUnchanged,
  action,
  raw,
  lineWidgets,
  pickAllLabels,
  paneLabels,
  viewOnly,
  content = '',
}: YamlDiffViewerProps) {
  const { t } = useTranslation()
  const { actualTheme, colorTheme } = useAppearance()
  const themeMode = actualTheme === 'dark' ? 'dark' : 'light'
  const backgroundColor = useMonacoBackgroundColor(
    '--background',
    themeMode,
    colorTheme
  )
  const editorRef = useRef<monacoEditor.IStandaloneDiffEditor | null>(null)
  const [diffMode, setDiffMode] = useState<DiffMode>('previous-vs-modified')
  const [editorMounted, setEditorMounted] = useState(false)
  const [collapseUnchanged, setCollapseUnchanged] = useState(
    hideUnchanged ?? false
  )
  // The diff (and with it the collapsed unchanged regions) is computed
  // asynchronously by Monaco; until the first computation lands the editor
  // would flash the full expanded documents, so it stays covered.
  const [diffReady, setDiffReady] = useState(false)
  // Seam overlay rows: one ← → pair per visible changed path, positioned by
  // the anchor line's viewport y. Rows whose line is scrolled out of the
  // viewport (Monaco virtual scrolling) simply drop out.
  const [pickRows, setPickRows] = useState<
    { widget: DiffLineWidget; top: number }[]
  >([])
  // Seam x in px from the container's left edge. The diff sash is draggable
  // and Monaco does not keep the split at 50%, so the overlay anchors to the
  // original editor's right edge instead of left-1/2.
  const [seamX, setSeamX] = useState<number | null>(null)
  const containerRef = useRef<HTMLDivElement | null>(null)

  const handleEditorDidMount = (editor: monacoEditor.IStandaloneDiffEditor) => {
    editorRef.current = editor
    // A remount (theme/collapse key change) recomputes the diff; restart the
    // ready cycle so the cover and the overlay resettle.
    setDiffReady(false)
    editor.onDidUpdateDiff(() => setDiffReady(true))
    // Monaco's empty-original fast path fires onDidUpdateDiff before this
    // subscription exists and the event is not replayed.
    if (editor.getOriginalEditor().getValue() === '') {
      setDiffReady(true)
    }
    setEditorMounted(true)
  }

  // In-diff side-pick overlay: a React layer over the diff container (own
  // pointer-events, nothing between mousedown and mouseup) whose rows track
  // the anchor lines through scrolls, layout changes and diff recomputes.
  // Position source is getScrolledVisiblePosition: viewport-relative, null
  // for out-of-view lines, so the sync only ever shows visible rows.
  useEffect(() => {
    const editor = editorRef.current
    if (
      !editorMounted ||
      !editor ||
      !diffReady ||
      !lineWidgets ||
      lineWidgets.length === 0
    ) {
      setPickRows([])
      return
    }
    const ROW_H = 22
    const sync = () => {
      const original = editor.getOriginalEditor()
      const modified = editor.getModifiedEditor()
      const container = containerRef.current
      const originalDom = original.getDomNode()
      if (container && originalDom) {
        setSeamX(
          originalDom.getBoundingClientRect().right -
            container.getBoundingClientRect().left
        )
      }
      const rows: { widget: DiffLineWidget; top: number }[] = []
      for (const widget of lineWidgets) {
        const candidates = [
          { ed: original, line: widget.originalLine },
          { ed: modified, line: widget.modifiedLine },
        ].filter((c) => c.line > 0)
        let top: number | null = null
        for (const candidate of candidates) {
          const pos = candidate.ed.getScrolledVisiblePosition({
            lineNumber: candidate.line,
            column: 1,
          })
          if (pos) {
            top = Math.max(0, pos.top + (pos.height - ROW_H) / 2)
            break
          }
        }
        if (top != null) rows.push({ widget, top })
      }
      setPickRows(rows)
    }
    const disposables = [
      editor.getOriginalEditor().onDidScrollChange(sync),
      editor.getModifiedEditor().onDidScrollChange(sync),
      editor.getOriginalEditor().onDidLayoutChange(sync),
      editor.getModifiedEditor().onDidLayoutChange(sync),
    ]
    sync()
    return () => disposables.forEach((d) => d.dispose())
    // backgroundColor/collapseUnchanged are part of the editor's remount key;
    // they re-run the effect against the fresh editors.
  }, [
    lineWidgets,
    editorMounted,
    diffReady,
    collapseUnchanged,
    backgroundColor,
  ])

  // Remove status field from YAML content
  const removeStatusField = (yamlContent: string): string => {
    if (!yamlContent.trim()) return yamlContent

    try {
      const parsed = yaml.load(yamlContent)
      if (parsed && typeof parsed === 'object') {
        // Remove status field recursively
        const removeStatus = (obj: unknown): unknown => {
          if (obj && typeof obj === 'object') {
            if (Array.isArray(obj)) {
              return obj.map(removeStatus)
            } else {
              const result: Record<string, unknown> = {}
              for (const [key, value] of Object.entries(obj)) {
                if (key !== 'status') {
                  result[key] = removeStatus(value)
                }
              }
              return result
            }
          }
          return obj
        }

        const cleaned = removeStatus(parsed)
        return yaml.dump(cleaned, { indent: 2, sortKeys: true })
      }
    } catch (error) {
      console.error('Failed to remove status field from YAML:', error)
    }

    return yamlContent
  }

  // Determine which content to show based on diff mode
  const getDiffContent = () => {
    if (diffMode === 'current-vs-modified' && current) {
      return {
        original: raw ? current : removeStatusField(current),
        modified: raw ? modified : removeStatusField(modified),
      }
    }
    return {
      original: raw ? original : removeStatusField(original),
      modified: raw ? modified : removeStatusField(modified),
    }
  }

  const { original: leftContent, modified: rightContent } = getDiffContent()

  const viewContent = viewOnly
    ? raw
      ? content
      : removeStatusField(content)
    : ''

  useEffect(() => {
    setDiffReady(false)
  }, [leftContent, rightContent])

  // Handle rollback button clicks
  const handleRollbackClick = (yamlContent: string) => {
    if (onRollback) {
      onRollback(yamlContent)
    }
  }

  // Shared Monaco wiring for the plain view and the diff view: theme naming,
  // loading node and the base options both editors agree on (the diff adds
  // its own split/hunk options on top).
  const editorTheme =
    actualTheme === 'dark'
      ? `custom-dark-${colorTheme}`
      : `custom-vs-${colorTheme}`
  const editorLoading = (
    <div
      className="flex h-full items-center justify-center text-muted-foreground"
      style={{ height }}
    >
      Loading editor...
    </div>
  )
  const defineEditorTheme = (
    monaco: Parameters<typeof defineMonacoBackgroundThemes>[0]
  ) =>
    defineMonacoBackgroundThemes(monaco, {
      darkThemeName: `custom-dark-${colorTheme}`,
      lightThemeName: `custom-vs-${colorTheme}`,
      backgroundColor,
    })
  const baseEditorOptions: monacoEditor.IStandaloneEditorConstructionOptions = {
    readOnly: true,
    minimap: { enabled: true },
    scrollBeyondLastLine: false,
    wordWrap: 'on',
    folding: true,
    fontSize: 14,
    fontFamily:
      "'Maple Mono',Monaco, 'Cascadia Code', 'Roboto Mono', Consolas, 'Courier New', monospace",
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="!max-w-6xl sm:!max-w-6xl max-h-[80vh] flex flex-col">
        <DialogHeader>
          <DialogTitle className="flex items-center justify-between">
            <span className="text-lg font-bold">{title}</span>
            <div className="flex items-center gap-2 mr-4">
              {!viewOnly &&
                hideUnchanged !== undefined &&
                leftContent.trim() !== '' && (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => setCollapseUnchanged((v) => !v)}
                  >
                    {collapseUnchanged
                      ? t('common.diff.showAllFields')
                      : t('common.diff.diffOnly')}
                  </Button>
                )}
              {!viewOnly && current && (
                <>
                  {diffMode === 'current-vs-modified' && (
                    <Button
                      onClick={() => handleRollbackClick(modified)}
                      disabled={isRollingBack}
                      variant="outline"
                      size="sm"
                    >
                      {isRollingBack
                        ? t('resourceHistory.rollback.rollingBack')
                        : t('resourceHistory.rollback.modified')}
                    </Button>
                  )}

                  {diffMode === 'previous-vs-modified' && (
                    <>
                      <Button
                        onClick={() => handleRollbackClick(original)}
                        disabled={isRollingBack}
                        variant="outline"
                        size="sm"
                      >
                        {isRollingBack
                          ? t('resourceHistory.rollback.rollingBack')
                          : t('resourceHistory.rollback.previous')}
                      </Button>
                      <Button
                        onClick={() => handleRollbackClick(modified)}
                        disabled={isRollingBack}
                        variant="outline"
                        size="sm"
                      >
                        {isRollingBack
                          ? t('resourceHistory.rollback.rollingBack')
                          : t('resourceHistory.rollback.modified')}
                      </Button>
                    </>
                  )}

                  <Select
                    value={diffMode}
                    onValueChange={(value: DiffMode) => setDiffMode(value)}
                  >
                    <SelectTrigger className="max-w-64">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="previous-vs-modified">
                        {t('resourceHistory.previousVsModified')}
                      </SelectItem>
                      <SelectItem value="current-vs-modified">
                        {t('resourceHistory.currentVsModified')}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </>
              )}
              {action}
            </div>
          </DialogTitle>
        </DialogHeader>
        {!viewOnly && paneLabels && (
          <div className="grid grid-cols-2 gap-2 pb-1.5">
            <span className="flex items-center justify-center gap-2 rounded-full border bg-muted/40 py-1 text-sm font-medium text-foreground">
              <span className="h-2.5 w-2.5 rounded-full bg-sky-500" />
              {paneLabels.original}
            </span>
            <span className="flex items-center justify-center gap-2 rounded-full border bg-muted/40 py-1 text-sm font-medium text-foreground">
              <span className="h-2.5 w-2.5 rounded-full bg-violet-500" />
              {paneLabels.modified}
            </span>
          </div>
        )}
        <div ref={containerRef} className="relative flex-1 min-h-0">
          {viewOnly ? (
            <MonacoEditor
              key={`yaml-diff-viewer-view-${colorTheme}-${actualTheme}-${backgroundColor}`}
              height={height}
              language="yaml"
              loading={editorLoading}
              beforeMount={defineEditorTheme}
              theme={editorTheme}
              options={baseEditorOptions}
              value={viewContent}
            />
          ) : leftContent.trim() !== '' && leftContent === rightContent ? (
            // Canonically identical sides (the normalization above already
            // erased key-order noise): two full columns would render with
            // zero highlights, so say so instead.
            <div
              className="flex items-center justify-center text-sm text-muted-foreground"
              style={{ height }}
            >
              {t('common.diff.identical')}
            </div>
          ) : (
            <>
              {!diffReady && (
                <div className="absolute inset-0 z-10 flex items-center justify-center gap-2 bg-background/70 text-sm text-muted-foreground">
                  <span className="h-4 w-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-primary" />
                  {t('common.diff.computing')}
                </div>
              )}
              <MonacoDiffEditor
                key={`yaml-diff-viewer-${colorTheme}-${actualTheme}-${backgroundColor}-${collapseUnchanged}`}
                height={height}
                language="yaml"
                loading={editorLoading}
                beforeMount={defineEditorTheme}
                theme={editorTheme}
                options={{
                  ...baseEditorOptions,
                  lineNumbers: 'relative',
                  renderSideBySide: true,
                  enableSplitViewResizing: true,
                  renderOverviewRuler: true,
                  overviewRulerBorder: true,
                  overviewRulerLanes: 2,
                  hideUnchangedRegions: {
                    enabled: collapseUnchanged,
                    // Collapsed bars stay clickable per region, so a small
                    // context plus a low minimum keeps untouched YAML hidden
                    // while each hunk can still be peeked at.
                    contextLineCount: 4,
                    minimumLineCount: 8,
                    revealLineCount: 20,
                  },
                }}
                onMount={handleEditorDidMount}
                original={leftContent}
                modified={rightContent}
              />
              {lineWidgets && lineWidgets.length > 0 && diffReady && (
                // Seam overlay: the rows float over the diff's sash line,
                // horizontally anchored to the live seam position (seamX —
                // the sash is draggable, so 50% is not the seam); z sits
                // above Monaco's sash (z-index 35) so its buttons keep
                // receiving clicks. The layer itself is pointer-transparent
                // so only the buttons eat clicks.
                <div className="pointer-events-none absolute inset-0 z-40">
                  {pickAllLabels && seamX != null && (
                    <div
                      className="pointer-events-auto absolute top-2 flex -translate-x-1/2 gap-0.5 rounded-full border bg-background/95 p-0.5 shadow-sm"
                      style={{ left: seamX }}
                    >
                      <button
                        type="button"
                        title={pickAllLabels.cluster}
                        className="flex h-6 w-7 items-center justify-center rounded-full text-muted-foreground hover:bg-accent hover:text-foreground"
                        onClick={() =>
                          lineWidgets.forEach((w) => w.onPick('cluster'))
                        }
                      >
                        <ChevronsLeft className="h-4 w-4" />
                      </button>
                      <button
                        type="button"
                        title={pickAllLabels.db}
                        className="flex h-6 w-7 items-center justify-center rounded-full text-muted-foreground hover:bg-accent hover:text-foreground"
                        onClick={() =>
                          lineWidgets.forEach((w) => w.onPick('db'))
                        }
                      >
                        <ChevronsRight className="h-4 w-4" />
                      </button>
                    </div>
                  )}
                  {pickRows.map(({ widget, top }) => (
                    <div
                      key={widget.path}
                      className="absolute flex -translate-x-1/2 gap-0.5"
                      style={{ top, left: seamX ?? undefined }}
                    >
                      {(['cluster', 'db'] as const).map((choice) => {
                        const active = widget.picked === choice
                        return (
                          <button
                            key={choice}
                            type="button"
                            className={`pointer-events-auto flex h-[22px] w-[22px] items-center justify-center rounded border text-xs leading-none shadow-sm ${
                              active
                                ? 'border-primary bg-primary text-primary-foreground'
                                : 'border-border bg-background text-muted-foreground hover:bg-accent hover:text-foreground'
                            }`}
                            title={`${widget.path} · ${
                              choice === 'cluster'
                                ? widget.clusterTitle
                                : widget.dbTitle
                            }`}
                            onClick={() => widget.onPick(choice)}
                          >
                            {choice === 'cluster' ? '←' : '→'}
                          </button>
                        )
                      })}
                    </div>
                  ))}
                </div>
              )}
            </>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}
