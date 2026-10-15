import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

export interface EnvVarRow {
  name: string
  value?: string
}

interface EnvEditorProps {
  env: EnvVarRow[]
  onChange: (env: EnvVarRow[]) => void
}

// Wizard env editor (design §3): plain name=value rows only — referenced
// variables (valueFrom/envFrom) are maintained in YAML mode. Rows with a
// name but empty value must survive round trips: the backend maps them back
// onto the manifest's original entry (e.g. valueFrom) by name.
// parseEnvLines turns pasted KEY=VALUE lines into rows, skipping blanks and
// # comments, stripping a leading "export "; unparsable lines are counted.
function parseEnvLines(text: string): { rows: EnvVarRow[]; invalid: number } {
  const rows: EnvVarRow[] = []
  let invalid = 0
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const body = line.startsWith('export ') ? line.slice(7).trim() : line
    const eq = body.indexOf('=')
    const name = eq > 0 ? body.slice(0, eq).trim() : ''
    if (!name) {
      invalid++
      continue
    }
    rows.push({ name, value: body.slice(eq + 1) })
  }
  return { rows, invalid }
}

export function EnvEditor({ env, onChange }: EnvEditorProps) {
  const { t } = useTranslation()
  const [pasteOpen, setPasteOpen] = useState(false)
  const [pasteText, setPasteText] = useState('')
  const [invalidCount, setInvalidCount] = useState(0)

  const update = (index: number, updates: Partial<EnvVarRow>) =>
    onChange(env.map((row, i) => (i === index ? { ...row, ...updates } : row)))

  const remove = (index: number) => onChange(env.filter((_, i) => i !== index))

  const add = () => onChange([...env, { name: '', value: '' }])

  const applyPaste = () => {
    const { rows, invalid } = parseEnvLines(pasteText)
    if (rows.length > 0) {
      const next = [...env]
      for (const row of rows) {
        const existing = next.findIndex(
          (candidate) => candidate.name === row.name
        )
        if (existing >= 0) next[existing] = row
        else next.push(row)
      }
      onChange(next)
    }
    setInvalidCount(invalid)
    if (invalid === 0) setPasteText('')
  }

  return (
    <div className="space-y-3 rounded-lg border p-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Label className="text-sm font-medium">
          {t('applications.wizard.envTitle')}
        </Label>
        <span className="text-xs text-muted-foreground">
          {t('common.fields.optional', 'Optional')}
        </span>
        <span className="text-xs text-muted-foreground xl:ml-auto">
          {t('applications.wizard.envHint')}
        </span>
      </div>
      {env.length > 0 && (
        <div className="space-y-2">
          {env.map((row, index) => (
            <div key={index} className="flex items-center gap-2">
              <Input
                value={row.name}
                onChange={(event) =>
                  update(index, { name: event.target.value })
                }
                placeholder={t('editors.variableName')}
                className="h-8 font-mono text-xs"
              />
              <Input
                value={row.value ?? ''}
                onChange={(event) =>
                  update(index, { value: event.target.value })
                }
                placeholder={t('editors.variableValue')}
                className="h-8 font-mono text-xs"
              />
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="shrink-0 px-2"
                title={t('applications.wizard.removeVariable')}
                onClick={() => remove(index)}
              >
                ✕
              </Button>
            </div>
          ))}
        </div>
      )}
      <div className="flex items-center gap-4">
        <button
          type="button"
          className="cursor-pointer text-xs text-muted-foreground transition-colors hover:text-foreground"
          onClick={add}
        >
          ＋ {t('applications.wizard.addVariable')}
        </button>
        <button
          type="button"
          className="cursor-pointer text-xs text-muted-foreground transition-colors hover:text-foreground"
          onClick={() => {
            setPasteOpen((prev) => !prev)
            setInvalidCount(0)
          }}
        >
          ＋ {t('applications.wizard.batchPaste')}
        </button>
      </div>
      {pasteOpen && (
        <div className="space-y-2">
          <textarea
            value={pasteText}
            onChange={(event) => setPasteText(event.target.value)}
            placeholder={t('applications.wizard.batchPastePlaceholder')}
            className="h-28 w-full resize-none rounded-md border bg-background p-3 font-mono text-xs outline-none placeholder:text-muted-foreground"
          />
          <div className="flex flex-wrap items-center gap-2">
            <Button type="button" size="sm" onClick={applyPaste}>
              {t('applications.wizard.applyPaste')}
            </Button>
            {invalidCount > 0 && (
              <span className="text-xs text-destructive">
                {t('applications.wizard.invalidLines', { count: invalidCount })}
              </span>
            )}
            <span className="text-xs text-muted-foreground">
              {t('applications.wizard.batchPasteHint')}
            </span>
          </div>
        </div>
      )}
    </div>
  )
}
