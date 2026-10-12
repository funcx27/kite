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

// One shared unit per resource kind: CPU is always cores (milli-core
// quantities like "500m" are converted 1:1000 for display), memory keeps a
// single Mi/Gi dropdown shared by the limit and request inputs of its row.
const MEMORY_UNITS = ['Mi', 'Gi'] as const
type MemoryUnit = (typeof MEMORY_UNITS)[number]

const MEMORY_UNIT_BYTES: Record<MemoryUnit, number> = {
  Mi: 1 << 20,
  Gi: 1 << 30,
}

// splitCPU renders a CPU quantity in cores; unknown shapes fall back blank.
function splitCPU(v: string | undefined): string {
  const m = /^(\d*\.?\d+)(m)?$/.exec((v || '').trim())
  if (!m) return ''
  if (!m[2]) return m[1]
  return String(Number((Number(m[1]) / 1000).toFixed(4)))
}

function memoryToBytes(v: string): number | undefined {
  const m = /^(\d+(?:\.\d+)?)(Ki|Mi|Gi|Ti|K|M|G|T|k|B)?$/.exec(v)
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

// trimNum keeps converted values readable (0.5, 1.25, 489) without float
// noise (0.4999999).
function trimNum(n: number): string {
  return String(Number(n.toFixed(4)))
}

// splitMemory renders a memory quantity as a number in the shared unit;
// decimal (M/G) or other legacy suffixes are converted through bytes.
function splitMemory(v: string | undefined, unit: MemoryUnit): string {
  const bytes = memoryToBytes((v || '').trim())
  if (bytes === undefined) return ''
  return trimNum(bytes / MEMORY_UNIT_BYTES[unit])
}

// displayUnit picks the row's shared unit from the limit first (the primary
// field), falling back to the request, then Mi.
function displayUnit(v: string | undefined): MemoryUnit {
  const m = /^(\d+(?:\.\d+)?)(Mi|Gi)$/.exec((v || '').trim())
  if (m && (m[2] === 'Mi' || m[2] === 'Gi')) return m[2]
  return 'Mi'
}

// requestExceedsLimit reports whether a filled request exceeds its limit —
// parsed through the same quantity shapes the editor accepts — so the rows
// show an inline error and the wizard gates Next on the same predicate.
// eslint-disable-next-line react-refresh/only-export-components
export function requestExceedsLimit(
  resources: Container['resources'] | undefined
): { cpu: boolean; memory: boolean } {
  const lim = resources?.limits
  const req = resources?.requests
  const cpuExceeds =
    !!lim?.cpu?.trim() &&
    !!req?.cpu?.trim() &&
    Number(splitCPU(req.cpu)) > Number(splitCPU(lim.cpu))
  const memLimitBytes = lim?.memory?.trim()
    ? memoryToBytes(lim.memory.trim())
    : undefined
  const memRequestBytes = req?.memory?.trim()
    ? memoryToBytes(req.memory.trim())
    : undefined
  const memoryExceeds =
    memLimitBytes !== undefined &&
    memRequestBytes !== undefined &&
    memRequestBytes > memLimitBytes
  return { cpu: cpuExceeds, memory: memoryExceeds }
}

// halfNum halves a number for the CPU overcommit default, trimming float
// noise (1 → 0.5, 501 → 250.5).
function halfNum(num: string): string {
  const n = Number(num)
  if (!Number.isFinite(n) || n === 0) return num
  return String(Number((n / 2).toFixed(3)))
}

// Requests follow the limits with the platform's overcommit profile — CPU
// at half the limit (compressible), memory at the full limit — until the
// user overrides one manually: an empty request — or one still equal to
// the previous default — tracks the new limit, an edited one keeps its
// value. Empty strings read as absent downstream (the backend re-defaults
// them with the same profile).
function defaultNum(kind: ResourceKind, limitNum: string): string {
  if (limitNum === '') return ''
  return kind === 'cpu' ? halfNum(limitNum) : limitNum
}

export function ResourceEditor({ container, onUpdate }: ResourceEditorProps) {
  const { t } = useTranslation()
  const limits = container.resources?.limits
  const requests = container.resources?.requests

  const cpuLimit = splitCPU(limits?.cpu)
  const cpuRequest = splitCPU(requests?.cpu)
  const memUnit = displayUnit(limits?.memory ?? requests?.memory)
  const memLimit = splitMemory(limits?.memory, memUnit)
  const memRequest = splitMemory(requests?.memory, memUnit)

  const writeCPU = (limitNum: string, requestNum: string) =>
    onUpdate({
      resources: {
        ...container.resources,
        requests: { ...requests, cpu: requestNum },
        limits: { ...limits, cpu: limitNum },
      },
    })

  const writeMemory = (
    limitNum: string,
    requestNum: string,
    unit: MemoryUnit
  ) =>
    onUpdate({
      resources: {
        ...container.resources,
        requests: {
          ...requests,
          memory: requestNum === '' ? '' : `${requestNum}${unit}`,
        },
        limits: {
          ...limits,
          memory: limitNum === '' ? '' : `${limitNum}${unit}`,
        },
      },
    })

  const updateLimitNum = (kind: ResourceKind, num: string) => {
    if (kind === 'cpu') {
      const follows =
        cpuRequest === '' || cpuRequest === defaultNum('cpu', cpuLimit)
      writeCPU(num, follows ? defaultNum('cpu', num) : cpuRequest)
      return
    }
    const follows =
      memRequest === '' || memRequest === defaultNum('memory', memLimit)
    writeMemory(num, follows ? defaultNum('memory', num) : memRequest, memUnit)
  }

  const updateRequestNum = (kind: ResourceKind, num: string) => {
    if (kind === 'cpu') {
      onUpdate({
        resources: {
          ...container.resources,
          requests: { ...requests, cpu: num },
        },
      })
      return
    }
    onUpdate({
      resources: {
        ...container.resources,
        requests: {
          ...requests,
          memory: num === '' ? '' : `${num}${memUnit}`,
        },
      },
    })
  }

  // Switching the shared memory unit converts both values to the equivalent
  // quantity in the new unit (512 Mi → 0.5 Gi), keeping the follow pairing.
  const updateMemoryUnit = (unit: MemoryUnit) => {
    if (unit === memUnit) return
    const convert = (num: string) =>
      num === ''
        ? ''
        : trimNum(
            (Number(num) * MEMORY_UNIT_BYTES[memUnit]) / MEMORY_UNIT_BYTES[unit]
          )
    writeMemory(convert(memLimit), convert(memRequest), unit)
  }

  const row = (
    label: string,
    kind: ResourceKind,
    limitNum: string,
    requestNum: string,
    limitAria: string,
    requestAria: string,
    unitSlot: React.ReactNode,
    errorText?: string
  ) => (
    <div>
      <Label className="text-sm">{label}</Label>
      <div className="mt-1.5 flex items-center gap-2">
        <Input
          aria-label={limitAria}
          type="number"
          min={0}
          step="any"
          className="w-28"
          value={limitNum}
          onChange={(e) => updateLimitNum(kind, e.target.value)}
          placeholder="0"
        />
        <span className="text-muted-foreground">/</span>
        <Input
          aria-label={requestAria}
          type="number"
          min={0}
          step="any"
          className="w-28"
          value={requestNum}
          onChange={(e) => updateRequestNum(kind, e.target.value)}
          placeholder="0"
        />
        {unitSlot}
      </div>
      {errorText && (
        <p className="mt-1 text-xs text-destructive">{errorText}</p>
      )}
    </div>
  )

  const exceeds = requestExceedsLimit(container.resources)

  const cpuRow = row(
    t('applications.editors.cpuRow'),
    'cpu',
    cpuLimit,
    cpuRequest,
    t('applications.editors.cpuLimit'),
    t('applications.editors.cpuRequest'),
    <span
      key="cpu-unit"
      className="w-10 text-center text-sm text-muted-foreground"
    >
      {t('applications.editors.cpuUnitCore')}
    </span>,
    exceeds.cpu ? t('applications.editors.requestExceedsCpu') : undefined
  )

  const memoryRow = row(
    t('applications.editors.memoryRow'),
    'memory',
    memLimit,
    memRequest,
    t('applications.editors.memoryLimit'),
    t('applications.editors.memoryRequest'),
    <Select
      key="mem-unit"
      value={memUnit}
      onValueChange={(value) => updateMemoryUnit(value as MemoryUnit)}
    >
      <SelectTrigger
        className="w-24"
        aria-label={t('applications.editors.memoryUnit')}
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {MEMORY_UNITS.map((unit) => (
          <SelectItem key={unit} value={unit}>
            {unit}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>,
    exceeds.memory ? t('applications.editors.requestExceedsMemory') : undefined
  )

  return (
    <div className="space-y-4 rounded-lg border p-4">
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
      <div className="space-y-4">
        {cpuRow}
        {memoryRow}
      </div>
    </div>
  )
}
