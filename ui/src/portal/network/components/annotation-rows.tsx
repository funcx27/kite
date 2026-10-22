import { type NetworkAnnotationKey } from '@/portal/network/types'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

export interface AnnotationRow {
  key: string
  value: string
}

interface AnnotationRowsProps {
  rows: AnnotationRow[]
  onChange: (rows: AnnotationRow[]) => void
  // Already filtered to the applicable class / family by the caller.
  catalog: NetworkAnnotationKey[]
}

// Rows editor for key/value annotations picked from the platform catalog.
// Selecting a key prefills its catalog defaultValue (editable, clearable);
// a key already chosen by another row is disabled, so duplicates are
// impossible by construction. Shared by the host annotation dialog and the
// route dialog's service annotation section.
export function AnnotationRows({
  rows,
  onChange,
  catalog,
}: AnnotationRowsProps) {
  const { t } = useTranslation()

  const update = (index: number, changes: Partial<AnnotationRow>) =>
    onChange(rows.map((row, i) => (i === index ? { ...row, ...changes } : row)))

  const remove = (index: number) => onChange(rows.filter((_, i) => i !== index))

  const add = () => onChange([...rows, { key: '', value: '' }])

  return (
    <div className="space-y-2">
      {rows.length === 0 && (
        <p className="text-xs text-muted-foreground">
          {t('network.annotationsDialog.empty')}
        </p>
      )}
      {rows.map((row, index) => {
        const help = catalog.find((entry) => entry.key === row.key)?.description
        return (
          <div key={index} className="space-y-1">
            <div className="flex items-center gap-2">
              {/* The key select fills the remaining row width and truncates:
                  catalog keys are long, so w-full inside a shrinkable wrapper
                  keeps the value field and remove button inside the dialog. */}
              <div className="min-w-0 flex-1">
                <Select
                  value={row.key}
                  onValueChange={(value) => {
                    const entry = catalog.find(
                      (candidate) => candidate.key === value
                    )
                    update(index, {
                      key: value,
                      value: entry?.defaultValue ?? '',
                    })
                  }}
                >
                  <SelectTrigger className="h-8 w-full font-mono text-xs">
                    <SelectValue
                      className="truncate"
                      placeholder={t(
                        'network.annotationsDialog.keyPlaceholder'
                      )}
                    />
                  </SelectTrigger>
                  <SelectContent>
                    {catalog.map((entry) => (
                      <SelectItem
                        key={entry.key}
                        value={entry.key}
                        disabled={rows.some((other) => other.key === entry.key)}
                        className="font-mono"
                      >
                        {entry.key}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <Input
                value={row.value}
                onChange={(event) =>
                  update(index, { value: event.target.value })
                }
                placeholder={t('network.annotationsDialog.valuePlaceholder')}
                className="h-8 w-28 shrink-0 font-mono text-xs"
              />
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="shrink-0 px-2"
                title={t('network.annotationsDialog.remove')}
                onClick={() => remove(index)}
              >
                ✕
              </Button>
            </div>
            {help && (
              <p className="pl-1 text-xs text-muted-foreground">{help}</p>
            )}
          </div>
        )
      })}
      <div className="flex items-center">
        <button
          type="button"
          className="cursor-pointer text-xs text-muted-foreground transition-colors hover:text-foreground"
          onClick={add}
        >
          ＋ {t('network.annotationsDialog.add')}
        </button>
      </div>
    </div>
  )
}
