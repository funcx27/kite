// Template-aware text field for the node property forms: wraps Input /
// Textarea and pops a completion menu when the text right before the caret
// ends in a trigger ($ or {{). Picking an entry replaces the trigger span
// with a full {{ref}} token; the engine only resolves {{...}}, so a $ left
// un-completed stays harmless literal text.
import { useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Command,
  CommandGroup,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import { Input } from '@/components/ui/input'
import { Popover, PopoverAnchor, PopoverContent } from '@/components/ui/popover'
import { Textarea } from '@/components/ui/textarea'

import { buildTemplateRefs } from '../lib'
import type { ParamSpec, WorkflowNode } from '../types'

export interface TemplateInputProps {
  value: string
  onValueChange: (value: string) => void
  params: ParamSpec[]
  nodes: WorkflowNode[]
  // Owning node id: its own outputs are excluded from the steps.* options.
  currentNodeId?: string
  // Offers the git.* refs (build-image nodes with a git source only).
  gitScope?: boolean
  multiline?: boolean
  placeholder?: string
  rows?: number
  className?: string
  disabled?: boolean
  onFocus?: React.FocusEventHandler<HTMLInputElement | HTMLTextAreaElement>
}

// Trigger span [start, end) inside `value`; end follows the caret while the
// menu is open, so picking an entry replaces everything typed since the
// trigger. null = menu closed.
interface TriggerSpan {
  start: number
  end: number
}

export function TemplateInput({
  value,
  onValueChange,
  params,
  nodes,
  currentNodeId,
  gitScope,
  multiline = false,
  placeholder,
  rows,
  className,
  disabled,
  onFocus,
}: TemplateInputProps) {
  const { t } = useTranslation()
  const fieldRef = useRef<HTMLInputElement | HTMLTextAreaElement | null>(null)
  const commandRef = useRef<HTMLDivElement | null>(null)
  const contentRef = useRef<HTMLDivElement | null>(null)
  const [trigger, setTrigger] = useState<TriggerSpan | null>(null)

  const refs = useMemo(
    () => buildTemplateRefs(params, nodes, currentNodeId, gitScope),
    [params, nodes, currentNodeId, gitScope]
  )
  const groups = useMemo(() => {
    const pick = (prefix: string) => refs.filter((r) => r.startsWith(prefix))
    return [
      { label: t('workflow.template.groupParams'), refs: pick('params.') },
      { label: t('workflow.template.groupGit'), refs: pick('git.') },
      { label: t('workflow.template.groupRun'), refs: pick('run.') },
      { label: t('workflow.template.groupSteps'), refs: pick('steps.') },
    ].filter((group) => group.refs.length > 0)
  }, [refs, t])

  const handleChange = (
    e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>
  ) => {
    const el = e.currentTarget
    onValueChange(el.value)
    const caret = el.selectionStart ?? el.value.length
    if (trigger) {
      const head = el.value.slice(trigger.start, caret)
      const stillOnTrigger =
        head !== '' && (head.startsWith('$') || head.startsWith('{{'))
      setTrigger(stillOnTrigger ? { start: trigger.start, end: caret } : null)
      return
    }
    if (el.value[caret - 1] === '$') {
      setTrigger({ start: caret - 1, end: caret })
    } else if (el.value.slice(caret - 2, caret) === '{{') {
      setTrigger({ start: caret - 2, end: caret })
    }
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (!trigger) return
    if (e.key === 'Escape') {
      setTrigger(null)
      return
    }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Enter') {
      e.preventDefault()
      // Focus stays in the text field; cmdk drives selection from keydowns
      // on its root element, so forward the event there.
      commandRef.current?.dispatchEvent(
        new KeyboardEvent('keydown', { key: e.key, bubbles: true })
      )
    }
  }

  // The menu never steals focus (item mousedown is prevented), so a blur
  // means the user went elsewhere; a focus target inside the menu keeps it.
  const handleBlur = (e: React.FocusEvent) => {
    if (
      e.relatedTarget instanceof Node &&
      contentRef.current?.contains(e.relatedTarget)
    ) {
      return
    }
    setTrigger(null)
  }

  const pickRef = (ref: string) => {
    const token = `{{${ref}}}`
    const start = trigger?.start ?? value.length
    const end = trigger?.end ?? start
    onValueChange(value.slice(0, start) + token + value.slice(end))
    setTrigger(null)
    // The controlled value commits asynchronously; restore the caret after
    // the re-render so typing continues right after the inserted token.
    window.setTimeout(() => {
      const el = fieldRef.current
      el?.focus()
      el?.setSelectionRange(start + token.length, start + token.length)
    }, 0)
  }

  const fieldProps = {
    ref: (el: HTMLInputElement | HTMLTextAreaElement | null) => {
      fieldRef.current = el
    },
    value,
    placeholder,
    className,
    disabled,
    onChange: handleChange,
    onKeyDown: handleKeyDown,
    onFocus,
    onBlur: handleBlur,
  }

  return (
    <Popover
      open={trigger !== null}
      onOpenChange={(open) => {
        if (!open) setTrigger(null)
      }}
    >
      <PopoverAnchor className="block w-full">
        {multiline ? (
          <Textarea {...fieldProps} rows={rows} />
        ) : (
          <Input {...fieldProps} />
        )}
      </PopoverAnchor>
      <PopoverContent
        ref={contentRef}
        align="start"
        sideOffset={6}
        className="w-[var(--radix-popover-trigger-width)] p-0"
        onOpenAutoFocus={(e) => e.preventDefault()}
        onCloseAutoFocus={(e) => e.preventDefault()}
      >
        <Command ref={commandRef}>
          <CommandList className="max-h-56">
            {groups.map((group) => (
              <CommandGroup key={group.label} heading={group.label}>
                {group.refs.map((ref) => (
                  <CommandItem
                    key={ref}
                    value={ref}
                    className="font-mono text-xs"
                    onMouseDown={(e) => e.preventDefault()}
                    onSelect={() => pickRef(ref)}
                  >
                    {ref}
                  </CommandItem>
                ))}
              </CommandGroup>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
