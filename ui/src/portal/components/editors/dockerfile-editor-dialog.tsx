// Full-size Monaco editor for an inline Dockerfile: the small inline field
// stays the quick-edit path, this dialog is the "zoom in" view for longer
// content. Copy is owned by the callers (feature namespaces): title, hint
// and the line-counter label arrive as props; only the shared common.*
// strings (cancel / save) resolve here.
import { useEffect, useState } from 'react'
import type { editor as monacoEditor } from 'monaco-editor'
import { useTranslation } from 'react-i18next'

import { MonacoEditor } from '@/lib/monaco-loader'
import {
  defineMonacoBackgroundThemes,
  useMonacoBackgroundColor,
} from '@/lib/monaco-theme'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useAppearance } from '@/components/appearance-provider'

export interface DockerfileEditorDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  value: string
  onCommit: (draft: string) => void
  /** Dialog title, supplied from the caller's feature namespace. */
  title?: string
  /** Footer hint shown beside the line counter (e.g. template syntax). */
  hint?: string
  /** Renders the live line counter, supplied from the caller's namespace. */
  lineCountLabel?: (count: number) => string
}

export function DockerfileEditorDialog({
  open,
  onOpenChange,
  value,
  onCommit,
  title,
  hint,
  lineCountLabel,
}: DockerfileEditorDialogProps) {
  const { t } = useTranslation()
  const { actualTheme, colorTheme } = useAppearance()
  const themeMode = actualTheme === 'dark' ? 'dark' : 'light'
  const backgroundColor = useMonacoBackgroundColor(
    '--background',
    themeMode,
    colorTheme
  )
  // Local draft: edits apply only on Save; reopening resets to the current
  // value so a cancelled edit never leaks into the next session.
  const [draft, setDraft] = useState(value)
  useEffect(() => {
    if (open) setDraft(value)
  }, [open, value])

  const lineCount = draft === '' ? 0 : draft.split('\n').length

  const editorOptions: monacoEditor.IStandaloneEditorConstructionOptions = {
    minimap: { enabled: false },
    wordWrap: 'on',
    scrollBeyondLastLine: false,
    lineNumbers: 'on',
    fontSize: 13,
    automaticLayout: true,
    fontFamily:
      "'Maple Mono', Monaco, 'Cascadia Code', 'Roboto Mono', Consolas, 'Courier New', monospace",
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{title ?? 'Dockerfile'}</DialogTitle>
        </DialogHeader>
        <div className="h-[60vh] min-h-0 overflow-hidden rounded-md border">
          <MonacoEditor
            key={`dockerfile-editor-${colorTheme}-${actualTheme}-${backgroundColor}`}
            height="100%"
            defaultLanguage="dockerfile"
            value={draft}
            onChange={(v) => setDraft(v ?? '')}
            loading={
              <div className="flex h-full items-center justify-center text-muted-foreground">
                Loading editor...
              </div>
            }
            beforeMount={(monaco) => {
              defineMonacoBackgroundThemes(monaco, {
                darkThemeName: `custom-dark-${colorTheme}`,
                lightThemeName: `custom-vs-${colorTheme}`,
                backgroundColor,
              })
            }}
            theme={
              actualTheme === 'dark'
                ? `custom-dark-${colorTheme}`
                : `custom-vs-${colorTheme}`
            }
            options={editorOptions}
          />
        </div>
        <DialogFooter className="items-center sm:justify-between">
          <div className="min-w-0 text-xs text-muted-foreground">
            {lineCountLabel && <span>{lineCountLabel(lineCount)}</span>}
            {lineCountLabel && hint && <span className="mx-1.5">·</span>}
            {hint}
          </div>
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {t('common.actions.cancel')}
            </Button>
            <Button
              onClick={() => {
                onCommit(draft)
                onOpenChange(false)
              }}
            >
              {t('common.actions.save')}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
