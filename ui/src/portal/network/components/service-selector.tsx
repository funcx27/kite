import { useState } from 'react'
import { Check, ChevronsUpDown, Loader2 } from 'lucide-react'

import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'

export interface ServiceOption {
  name: string
  description?: string
}

interface ServiceSelectorProps {
  value: string
  onChange: (name: string) => void
  options: ServiceOption[]
  loading?: boolean
  disabled?: boolean
  placeholder: string
  searchPlaceholder: string
  emptyText: string
  customText: string
}

// Name picker over live cluster objects with a free-text fallback row: the
// target may not exist yet (a service is created after the route), so a
// typed name that matches nothing stays selectable.
export function ServiceSelector({
  value,
  onChange,
  options,
  loading = false,
  disabled = false,
  placeholder,
  searchPlaceholder,
  emptyText,
  customText,
}: ServiceSelectorProps) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')

  const trimmed = query.trim()
  const exactMatch = options.some((option) => option.name === trimmed)
  const showCustom = trimmed !== '' && !exactMatch

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="outline"
          role="combobox"
          aria-expanded={open}
          disabled={disabled}
          className="w-full justify-between font-mono"
        >
          <span className="truncate">{value || placeholder}</span>
          <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        className="w-[var(--radix-popover-trigger-width)] p-0"
        align="start"
      >
        <Command shouldFilter={false}>
          <CommandInput
            placeholder={searchPlaceholder}
            className="h-9"
            value={query}
            onValueChange={setQuery}
          />
          <CommandList className="max-h-[min(50dvh,300px)]">
            {loading ? (
              <div className="flex items-center justify-center p-6 text-sm">
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              </div>
            ) : (
              <>
                {options.length === 0 && !showCustom && (
                  <CommandEmpty>{emptyText}</CommandEmpty>
                )}
                {options.length > 0 && (
                  <CommandGroup>
                    {options.map((option) => (
                      <CommandItem
                        key={option.name}
                        value={option.name}
                        onSelect={() => {
                          onChange(option.name)
                          setOpen(false)
                          setQuery('')
                        }}
                      >
                        <Check
                          className={cn(
                            'mr-2 h-4 w-4 shrink-0',
                            value === option.name ? 'opacity-100' : 'opacity-0'
                          )}
                        />
                        <span
                          className="truncate font-mono"
                          title={option.name}
                        >
                          {option.name}
                        </span>
                        {option.description && (
                          <span className="ml-2 truncate text-xs text-muted-foreground">
                            {option.description}
                          </span>
                        )}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                )}
                {showCustom && (
                  <CommandGroup>
                    <CommandItem
                      value={`__custom__${trimmed}`}
                      onSelect={() => {
                        onChange(trimmed)
                        setOpen(false)
                        setQuery('')
                      }}
                    >
                      <Check className="mr-2 h-4 w-4 shrink-0 opacity-0" />
                      <span className="truncate font-mono">{trimmed}</span>
                      <span className="ml-2 text-xs text-muted-foreground">
                        {customText}
                      </span>
                    </CommandItem>
                  </CommandGroup>
                )}
              </>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
