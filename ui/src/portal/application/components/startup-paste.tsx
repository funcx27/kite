import { useEffect, useRef, useState } from 'react'
import { Sparkles } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

import type { EnvVarRow } from './env-editor'

interface StartupPasteProps {
  command: string
  args: string
  env: EnvVarRow[]
  onApply: (command: string, args: string, env: EnvVarRow[]) => void
}

interface ParsedStartup {
  env: EnvVarRow[]
  hasCommand: boolean
  command: string
  args: string[]
}

const ENV_ASSIGNMENT_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*=/

// Classify pasted text: a multi-line paste whose every line is KEY=VALUE is
// a .env file — each line becomes one row with the raw text after `=` as the
// value, so unquoted spaces survive. Anything else parses as a shell command
// line (single token values, quotes stripped) where leading NAME=VALUE words
// route to env, the first command word becomes the command and the rest
// become args. The two grammars are indistinguishable for a single line like
// `B=hello world`; the preview makes whichever reading applies visible.
function parsePaste(text: string): ParsedStartup | null {
  const lines = text
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '' && !line.startsWith('#'))
  if (lines.length === 0) return null
  const stripAssignment = (line: string) => {
    const body = line.startsWith('export ') ? line.slice(7).trim() : line
    const eq = body.indexOf('=')
    return {
      name: body.slice(0, eq),
      value: stripQuotes(body.slice(eq + 1)),
    }
  }
  if (
    lines.length > 1 &&
    lines.every((line) =>
      ENV_ASSIGNMENT_PATTERN.test(
        line.startsWith('export ') ? line.slice(7).trim() : line
      )
    )
  ) {
    return {
      env: lines.map(stripAssignment),
      hasCommand: false,
      command: '',
      args: [],
    }
  }
  const tokens = parseShellTokens(text)
  if (tokens.length === 0) return null
  const { env, rest } = takeLeadingAssignments(tokens)
  return {
    env,
    hasCommand: rest.length > 0,
    command: rest[0] ?? '',
    args: rest.slice(1),
  }
}

function stripQuotes(value: string): string {
  if (value.length >= 2) {
    const quote = value[0]
    if ((quote === '"' || quote === "'") && value.endsWith(quote)) {
      return value.slice(1, -1)
    }
  }
  return value
}

// Quote-aware split for pasted shell-style text: whitespace (including
// newlines) separates words; double or single quotes keep inner whitespace
// inside one word (quotes are dropped, shell-style); whitespace inside
// $( ... ) does not split either; # at a word boundary comments out the rest
// of the line.
function parseShellTokens(text: string): string[] {
  const tokens: string[] = []
  let current = ''
  let hasToken = false
  let quote: '"' | "'" | null = null
  let substDepth = 0
  for (let i = 0; i < text.length; i++) {
    const char = text[i]
    if (quote) {
      if (char === quote) quote = null
      else current += char
    } else if (char === '"' || char === "'") {
      quote = char
      hasToken = true
    } else if (char === '$' && text[i + 1] === '(') {
      current += char
      hasToken = true
      substDepth++
    } else if (substDepth > 0 && char === '(') {
      current += char
    } else if (substDepth > 0 && char === ')') {
      substDepth--
      current += char
    } else if (char === '#' && substDepth === 0 && !hasToken) {
      while (i < text.length && text[i] !== '\n') i++
    } else if (/\s/.test(char) && substDepth === 0) {
      if (hasToken) {
        tokens.push(current)
        current = ''
        hasToken = false
      }
    } else {
      current += char
      hasToken = true
    }
  }
  if (hasToken) tokens.push(current)
  return tokens
}

// Leading NAME=VALUE words are shell env assignments for the command, not
// arguments: they route to the env editor. A standalone `export` before an
// assignment is consumed too; assignments after the command word stay args,
// matching shell semantics.
function takeLeadingAssignments(tokens: string[]): {
  env: EnvVarRow[]
  rest: string[]
} {
  const env: EnvVarRow[] = []
  let i = 0
  while (i < tokens.length) {
    if (
      tokens[i] === 'export' &&
      tokens[i + 1] &&
      ENV_ASSIGNMENT_PATTERN.test(tokens[i + 1])
    ) {
      i++
      continue
    }
    if (!ENV_ASSIGNMENT_PATTERN.test(tokens[i])) break
    const eq = tokens[i].indexOf('=')
    env.push({ name: tokens[i].slice(0, eq), value: tokens[i].slice(eq + 1) })
    i++
  }
  return { env, rest: tokens.slice(i) }
}

// Step-level batch paste (wizard step 2): the single entry that fills the
// startup command/args card and the env card. Paste is parsed shell-style
// and previewed by classification — leading NAME=VALUE words become env
// rows, the first command word becomes the command and the rest become args
// (k8s ENTRYPOINT/CMD mapping); a pure assignment list leaves the current
// command/args untouched. Nothing is written before [应用到表单].
export function StartupPaste({
  command,
  args,
  env,
  onApply,
}: StartupPasteProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const [parsed, setParsed] = useState<ParsedStartup | null>(null)
  const [emptyResult, setEmptyResult] = useState(false)
  const pasteRef = useRef<HTMLTextAreaElement | null>(null)

  // Fit the paste box to its content: grows with pasted lines up to the
  // max-h cap, where internal scrolling takes over.
  useEffect(() => {
    const el = pasteRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${el.scrollHeight}px`
  }, [text, open])

  const runParse = () => {
    const parsed = parsePaste(text)
    setEmptyResult(parsed === null)
    setParsed(parsed)
  }

  const apply = () => {
    if (!parsed) return
    // Same-name rows are overwritten, matching the previous env batch paste.
    const nextEnv = [...env]
    for (const pair of parsed.env) {
      const existing = nextEnv.findIndex((row) => row.name === pair.name)
      if (existing >= 0) nextEnv[existing] = pair
      else nextEnv.push(pair)
    }
    onApply(
      parsed.hasCommand ? parsed.command : command,
      parsed.hasCommand ? parsed.args.join('\n') : args,
      nextEnv
    )
    setText('')
    setParsed(null)
    setOpen(false)
  }

  const close = () => {
    setText('')
    setParsed(null)
    setEmptyResult(false)
    setOpen(false)
  }

  return (
    <>
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => setOpen(true)}
      >
        <Sparkles className="mr-1.5 h-3.5 w-3.5" />
        {t('applications.wizard.smartParse')}
      </Button>
      <Dialog
        open={open}
        onOpenChange={(next) => (next ? setOpen(true) : close())}
      >
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t('applications.wizard.smartParse')}</DialogTitle>
            <DialogDescription className="sr-only">
              {t('applications.wizard.pasteHint')}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <textarea
              ref={pasteRef}
              autoFocus
              value={text}
              onChange={(event) => setText(event.target.value)}
              placeholder={t('applications.wizard.pastePlaceholder')}
              className="min-h-24 max-h-72 w-full resize-none overflow-auto rounded-md border bg-background p-3 font-mono text-xs outline-none placeholder:text-muted-foreground"
            />
            <div className="flex flex-wrap items-center gap-2">
              <Button type="button" size="sm" onClick={runParse}>
                {t('applications.wizard.parsePreview')}
              </Button>
              {emptyResult && (
                <span className="text-xs text-muted-foreground">
                  {t('applications.wizard.previewEmptyResult')}
                </span>
              )}
              <span className="text-xs text-muted-foreground">
                {t('applications.wizard.pasteHint')}
              </span>
            </div>
            {parsed && (
              <div className="space-y-2 rounded-md border bg-muted/30 p-3 text-xs">
                <div className="font-medium">
                  {t('applications.wizard.previewResultTitle')}
                </div>
                {parsed.env.length > 0 && (
                  <div>
                    <div className="text-muted-foreground">
                      {t('applications.wizard.previewEnv', {
                        count: parsed.env.length,
                      })}
                    </div>
                    <div className="mt-1 max-h-32 space-y-0.5 overflow-auto font-mono">
                      {parsed.env.map((row) => (
                        <div key={row.name}>
                          {row.name}={row.value}
                        </div>
                      ))}
                    </div>
                  </div>
                )}
                <div className="flex gap-2">
                  <span className="w-20 shrink-0 text-muted-foreground">
                    {t('applications.wizard.command')}
                  </span>
                  <span className="whitespace-pre-wrap font-mono">
                    {parsed.hasCommand
                      ? parsed.command
                      : t('applications.wizard.previewUnchanged')}
                  </span>
                </div>
                <div className="flex gap-2">
                  <span className="w-20 shrink-0 text-muted-foreground">
                    {t('applications.wizard.args')}
                  </span>
                  <span className="whitespace-pre-wrap font-mono">
                    {parsed.hasCommand
                      ? parsed.args.join('\n') ||
                        t('applications.wizard.previewNone')
                      : t('applications.wizard.previewUnchanged')}
                  </span>
                </div>
              </div>
            )}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" size="sm" onClick={close}>
              {t('common.actions.cancel', 'Cancel')}
            </Button>
            <Button type="button" size="sm" disabled={!parsed} onClick={apply}>
              {t('applications.wizard.applyToForm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
