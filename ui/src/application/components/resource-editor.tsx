import { Container } from 'kubernetes-types/core/v1'
import { useTranslation } from 'react-i18next'

import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

interface ResourceEditorProps {
  container: Container
  onUpdate: (updates: Partial<Container>) => void
}

type ResourceKind = 'cpu' | 'memory'

// A quantity split into its number and unit for the number+unit inputs.
interface SplitValue {
  num: string
  unit: string
}

const CPU_UNITS = ['m', 'core'] as const
const MEMORY_UNITS = ['Mi', 'Gi'] as const

// splitCPU breaks a CPU quantity ("500m", "2") into number + unit; unknown
// shapes fall back to a blank milli field.
function splitCPU(v: string | undefined): SplitValue {
  const m = /^(\d*\.?\d+)(m)?$/.exec((v || '').trim())
  if (!m) return { num: '', unit: 'm' }
  return { num: m[1], unit: m[2] === 'm' ? 'm' : 'core' }
}

function memoryToBytes(v: string): number | undefined {
  const m = /^(\d*\.?\d+)(Ki|Mi|Gi|Ti|K|M|G|T|k|B)?$/.exec(v)
  if (!m) return undefined
  const mult: Record<string, number> = {
    Ki: 1 << 10,
    Mi: 1 << 20,
    Gi: 1 << 30,
    Ti: 1 << 40,
    k: 1e3,
    K: 1e3,
    M: 1e6,
    G: 1e9,
    T: 1e12,
    B: 1,
    '': 1,
  }
  return Number(m[1]) * mult[m[2] ?? '']
}

// splitMemory breaks a binary memory quantity into number + unit; decimal
// (M/G) or plain-byte legacy values are converted to whole Mi.
function splitMemory(v: string | undefined): SplitValue {
  const raw = (v || '').trim()
  const m = /^(\d+(?:\.\d+)?)(Mi|Gi)$/.exec(raw)
  if (m) return { num: m[1], unit: m[2] }
  const bytes = memoryToBytes(raw)
  if (bytes === undefined) return { num: '', unit: 'Mi' }
  return { num: String(Math.round(bytes / (1 << 20))), unit: 'Mi' }
}

function mergeCPU(s: SplitValue): string {
  return s.num === '' ? '' : s.num + (s.unit === 'm' ? 'm' : '')
}

function mergeMemory(s: SplitValue): string {
  return s.num === '' ? '' : `${s.num}${s.unit}`
}

function mergeQuantity(kind: ResourceKind, s: SplitValue): string {
  return kind === 'cpu' ? mergeCPU(s) : mergeMemory(s)
}

function splitQuantity(kind: ResourceKind, v: string | undefined): SplitValue {
  return kind === 'cpu' ? splitCPU(v) : splitMemory(v)
}

// halfNum halves a number for the CPU overcommit default, trimming float
// noise (1 → 0.5, 501 → 250.5).
function halfNum(num: string): string {
  const n = Number(num)
  if (!Number.isFinite(n) || n === 0) return num
  return String(Number((n / 2).toFixed(3)))
}

export function ResourceEditor({ container, onUpdate }: ResourceEditorProps) {
  const { t } = useTranslation()
  const limits = container.resources?.limits
  const requests = container.resources?.requests

  // Requests follow the limits with the platform's overcommit profile — CPU
  // at half the limit (compressible), memory at the full limit — until the
  // user overrides one manually: an empty request — or one still equal to
  // the previous default — tracks the new limit, an edited one keeps its
  // value. Empty strings read as absent downstream (the backend re-defaults
  // them with the same profile).
  const defaultSplit = (kind: ResourceKind, limit: SplitValue): SplitValue => {
    if (limit.num === '') return { num: '', unit: limit.unit }
    return kind === 'cpu'
      ? { num: halfNum(limit.num), unit: limit.unit }
      : { ...limit }
  }

  const updateLimit = (kind: ResourceKind, next: SplitValue) => {
    const prev = splitQuantity(kind, limits?.[kind])
    const nextRequests = { ...requests }
    const current = requests?.[kind] || ''
    if (
      current === '' ||
      current === mergeQuantity(kind, defaultSplit(kind, prev))
    ) {
      nextRequests[kind] = mergeQuantity(kind, defaultSplit(kind, next))
    }
    onUpdate({
      resources: {
        ...container.resources,
        requests: nextRequests,
        limits: {
          ...limits,
          [kind]: mergeQuantity(kind, next),
        },
      },
    })
  }

  const updateRequest = (kind: ResourceKind, next: SplitValue) => {
    onUpdate({
      resources: {
        ...container.resources,
        requests: {
          ...requests,
          [kind]: mergeQuantity(kind, next),
        },
      },
    })
  }

  const field = (
    id: string,
    label: string,
    kind: ResourceKind,
    value: SplitValue,
    onNum: (num: string) => void,
    onUnit: (unit: string) => void
  ) => (
    <div>
      <Label htmlFor={id} className="text-sm">
        {label}
      </Label>
      <div className="mt-1.5 flex gap-2">
        <Input
          id={id}
          type="number"
          min={0}
          step="any"
          className="w-28"
          value={value.num}
          onChange={(e) => onNum(e.target.value)}
          placeholder="0"
        />
        <Select value={value.unit} onValueChange={onUnit}>
          <SelectTrigger className="w-24">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(kind === 'cpu' ? CPU_UNITS : MEMORY_UNITS).map((unit) => (
              <SelectItem key={unit} value={unit}>
                {kind === 'cpu' && unit === 'core'
                  ? t('applications.editors.cpuUnitCore')
                  : unit}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </div>
  )

  const onLimitNum = (kind: ResourceKind, prev: SplitValue) => (num: string) =>
    updateLimit(kind, { ...prev, num })
  const onLimitUnit =
    (kind: ResourceKind, prev: SplitValue) => (unit: string) =>
      updateLimit(kind, { ...prev, unit })
  const onRequestNum =
    (kind: ResourceKind, prev: SplitValue) => (num: string) =>
      updateRequest(kind, { ...prev, num })
  const onRequestUnit =
    (kind: ResourceKind, prev: SplitValue) => (unit: string) =>
      updateRequest(kind, { ...prev, unit })

  const cpuLimit = splitCPU(limits?.cpu)
  const memLimit = splitMemory(limits?.memory)
  const cpuRequest = splitCPU(requests?.cpu)
  const memRequest = splitMemory(requests?.memory)

  return (
    <div className="space-y-4 p-4 border rounded-lg">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Label className="text-sm font-medium">
          {t('applications.editors.resourceLimits')}
        </Label>
        <span className="text-xs text-destructive">
          {t('applications.editors.limitsRequiredTag')}
        </span>
        <span className="text-xs text-muted-foreground xl:ml-auto">
          {t('applications.editors.requestsFollowHint')}
        </span>
      </div>
      <div className="grid gap-x-8 gap-y-4 sm:grid-cols-2">
        {field(
          'cpu-limit',
          t('applications.editors.cpuLimit'),
          'cpu',
          cpuLimit,
          onLimitNum('cpu', cpuLimit),
          onLimitUnit('cpu', cpuLimit)
        )}
        {field(
          'cpu-request',
          t('applications.editors.cpuRequest'),
          'cpu',
          cpuRequest,
          onRequestNum('cpu', cpuRequest),
          onRequestUnit('cpu', cpuRequest)
        )}
        {field(
          'memory-limit',
          t('applications.editors.memoryLimit'),
          'memory',
          memLimit,
          onLimitNum('memory', memLimit),
          onLimitUnit('memory', memLimit)
        )}
        {field(
          'memory-request',
          t('applications.editors.memoryRequest'),
          'memory',
          memRequest,
          onRequestNum('memory', memRequest),
          onRequestUnit('memory', memRequest)
        )}
      </div>
    </div>
  )
}
