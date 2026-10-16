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
// onto the manifest's original entry (e.g. valueFrom) by name. Batch paste
// lives at the step level (startup-paste.tsx).
export function EnvEditor({ env, onChange }: EnvEditorProps) {
  const { t } = useTranslation()

  const update = (index: number, updates: Partial<EnvVarRow>) =>
    onChange(env.map((row, i) => (i === index ? { ...row, ...updates } : row)))

  const remove = (index: number) => onChange(env.filter((_, i) => i !== index))

  const add = () => onChange([...env, { name: '', value: '' }])

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
      <div className="flex items-center">
        <button
          type="button"
          className="cursor-pointer text-xs text-muted-foreground transition-colors hover:text-foreground"
          onClick={add}
        >
          ＋ {t('applications.wizard.addVariable')}
        </button>
      </div>
    </div>
  )
}
