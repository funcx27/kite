import { Check, Layers } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  DropdownMenuItem,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
} from '@/components/ui/dropdown-menu'

import { useWorkMode } from './work-mode-context'

// The work-mode radio group, inserted into the host user menu as its single
// integration point there (see modules/README.md).
export function WorkModeMenuItem() {
  const { t } = useTranslation()
  const { mode, setMode } = useWorkMode()

  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger>
        <Layers className="mr-2 h-4 w-4" />
        <span>{t('workMode.label')}</span>
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent>
        <DropdownMenuItem
          onClick={() => setMode('application')}
          role="menuitemradio"
          aria-checked={mode === 'application'}
          className={`flex items-center justify-between gap-2 cursor-pointer ${
            mode === 'application' ? 'font-medium text-foreground' : ''
          }`}
        >
          <span>{t('workMode.application')}</span>
          {mode === 'application' && <Check className="h-4 w-4 text-primary" />}
        </DropdownMenuItem>
        <DropdownMenuItem
          onClick={() => setMode('professional')}
          role="menuitemradio"
          aria-checked={mode === 'professional'}
          className={`flex items-center justify-between gap-2 cursor-pointer ${
            mode === 'professional' ? 'font-medium text-foreground' : ''
          }`}
        >
          <span>{t('workMode.professional')}</span>
          {mode === 'professional' && (
            <Check className="h-4 w-4 text-primary" />
          )}
        </DropdownMenuItem>
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  )
}
