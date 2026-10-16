// YAML-mode pane of the workflow editor: a monospace textarea plus the
// import-file / download actions. Parsing and applying the text back onto
// the draft live in the editor page (mode switch and save); this pane only
// owns the text content.
import { useRef } from 'react'
import { downloadTextFile } from '@/portal/components/download'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'

export function WorkflowYamlPane({
  value,
  onChange,
  getFilename,
  blocker,
}: {
  value: string
  onChange: (value: string) => void
  // Lazy download filename: the page resolves the envelope name at click
  // time, so no render ever parses the document just for a name.
  getFilename: () => string
  // Save-validation problem with the current draft; non-null disables the
  // download button and is surfaced as its title.
  blocker: string | null
}) {
  const { t } = useTranslation()
  const fileInputRef = useRef<HTMLInputElement>(null)

  const handleFile = async (file: File | undefined) => {
    if (!file) return
    onChange(await file.text())
  }

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-2 p-3">
      <div className="flex items-center gap-2">
        <input
          ref={fileInputRef}
          type="file"
          accept=".yaml,.yml,text/yaml"
          className="hidden"
          onChange={(event) => {
            void handleFile(event.target.files?.[0])
            // Reset so picking the same file twice still fires onChange.
            event.target.value = ''
          }}
        />
        <Button
          variant="outline"
          size="sm"
          onClick={() => fileInputRef.current?.click()}
        >
          {t('workflow.editor.yaml.importFile')}
        </Button>
        <Button
          variant="outline"
          size="sm"
          disabled={blocker !== null}
          title={blocker ?? undefined}
          onClick={() => downloadTextFile(getFilename(), value, 'text/yaml')}
        >
          {t('workflow.editor.yaml.download')}
        </Button>
      </div>
      <Textarea
        value={value}
        onChange={(event) => onChange(event.target.value)}
        spellCheck={false}
        className="min-h-0 flex-1 resize-none font-mono text-xs leading-5"
      />
    </div>
  )
}
