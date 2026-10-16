import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

interface StartupEditorProps {
  command: string
  args: string
  onChange: (command: string, args: string) => void
}

// Startup command/args editor (wizard step 2), following the EnvEditor
// interaction: with no content only the header plus an add link shows; the
// textareas appear once added or filled by the step-level batch paste. They
// hold the standard format — one array element per line, exactly what the
// workload spec stores.
export function StartupEditor({ command, args, onChange }: StartupEditorProps) {
  const { t } = useTranslation()
  const hasContent = command.trim() !== '' || args.trim() !== ''
  // The dialog remounts step content on open, so seeding from props is safe.
  const [editing, setEditing] = useState(hasContent)
  const expanded = hasContent || editing

  return (
    <div className="space-y-4 rounded-lg border p-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Label className="text-sm font-medium">
          {t('applications.wizard.startupTitle')}
        </Label>
        <span className="text-xs text-muted-foreground">
          {t('common.fields.optional', 'Optional')}
        </span>
        <span className="text-xs text-muted-foreground xl:ml-auto">
          {t('applications.wizard.startupHint')}
        </span>
      </div>
      {expanded && (
        <>
          <div>
            <Label htmlFor="wizard-command" className="font-mono">
              {t('applications.wizard.command')}
            </Label>
            <Textarea
              id="wizard-command"
              value={command}
              onChange={(event) => onChange(event.target.value, args)}
              placeholder={t('applications.wizard.commandPlaceholder')}
              className="mt-1.5 h-20 resize-none font-mono text-xs"
            />
            <p className="mt-1 text-xs text-muted-foreground">
              {t('applications.wizard.commandHint')}
            </p>
          </div>
          <div>
            <Label htmlFor="wizard-args" className="font-mono">
              {t('applications.wizard.args')}
            </Label>
            <Textarea
              id="wizard-args"
              value={args}
              onChange={(event) => onChange(command, event.target.value)}
              placeholder={t('applications.wizard.argsPlaceholder')}
              className="mt-1.5 h-24 resize-none font-mono text-xs"
            />
            <p className="mt-1 text-xs text-muted-foreground">
              {t('applications.wizard.argsHint')}
            </p>
          </div>
          <div className="flex items-center">
            <button
              type="button"
              className="cursor-pointer text-xs text-muted-foreground transition-colors hover:text-foreground"
              onClick={() => {
                onChange('', '')
                setEditing(false)
              }}
            >
              － {t('applications.wizard.removeStartup')}
            </button>
          </div>
        </>
      )}
      {!expanded && (
        <div className="flex items-center">
          <button
            type="button"
            className="cursor-pointer text-xs text-muted-foreground transition-colors hover:text-foreground"
            onClick={() => setEditing(true)}
          >
            ＋ {t('applications.wizard.addStartup')}
          </button>
        </div>
      )}
    </div>
  )
}
