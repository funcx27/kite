import { useEffect, useMemo, useRef, useState } from 'react'
import { useSpaces } from '@/platform/space-context'
import { ChevronDown, Layers, Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { getCurrentCluster } from '@/lib/current-cluster'
import { translateError } from '@/lib/utils'
import { useCluster } from '@/hooks/use-cluster'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

const dns1123LabelPattern = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

// Space switch entry of the application mode (design §11). Lives in the
// applications page header so the current space stays visible and adjustable
// even with the sidebar collapsed. The popover carries the substring search,
// the scroll list with the current space highlighted, an "all spaces" item
// and a create entry that labels the new namespace as a space. The create
// dialog picks the cluster explicitly: a namespace only ever exists on one
// cluster, and the global switcher is not visible in application mode.
export function SpacePicker({ defaultCluster }: { defaultCluster?: string }) {
  const { t } = useTranslation()
  const { spaces, currentSpace, setCurrentSpace, createSpace } = useSpaces()
  const { clusters } = useCluster()
  const availableClusters = useMemo(
    () => clusters.filter((candidate) => !candidate.error),
    [clusters]
  )
  const [pickerOpen, setPickerOpen] = useState(false)
  const [search, setSearch] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState('')
  const [nameError, setNameError] = useState('')
  const [cluster, setCluster] = useState('')
  const [isCreating, setIsCreating] = useState(false)
  const listRef = useRef<HTMLDivElement>(null)

  const filteredSpaces = useMemo(() => {
    const q = search.trim().toLowerCase()
    if (!q) return spaces
    return spaces.filter((space) => space.includes(q))
  }, [spaces, search])

  // Keep the current space visible in the scroll list.
  useEffect(() => {
    listRef.current
      ?.querySelector('[data-active="true"]')
      ?.scrollIntoView({ block: 'nearest' })
  }, [currentSpace, filteredSpaces.length])

  const resetCreateDialog = () => {
    setName('')
    setNameError('')
    setCreateOpen(false)
  }

  const openCreateDialog = () => {
    // Default: the header cluster filter when it pins one cluster, else the
    // global switcher when still available, else the first cluster.
    const current = getCurrentCluster()
    const fallback =
      defaultCluster ??
      (current && availableClusters.some((c) => c.name === current)
        ? current
        : undefined) ??
      availableClusters[0]?.name ??
      ''
    setCluster(fallback)
    setCreateOpen(true)
  }

  const handleCreate = async () => {
    const trimmed = name.trim()
    if (!dns1123LabelPattern.test(trimmed) || trimmed.length > 63) {
      setNameError(t('sidebar.spaces.invalidName'))
      return
    }
    if (spaces.includes(trimmed)) {
      setNameError(t('sidebar.spaces.nameExists'))
      return
    }
    if (!cluster) {
      setNameError(t('sidebar.spaces.clusterRequired'))
      return
    }
    setIsCreating(true)
    try {
      await createSpace(trimmed, cluster)
      toast.success(
        t('sidebar.spaces.createSuccess', { name: trimmed, cluster })
      )
      resetCreateDialog()
    } catch (error) {
      toast.error(translateError(error, t))
    } finally {
      setIsCreating(false)
    }
  }

  const selectSpace = (space: string | null) => {
    setCurrentSpace(space)
    setPickerOpen(false)
  }

  const spaceRowClass = (active: boolean) =>
    `flex w-full items-center rounded-md px-2 py-1.5 text-sm transition-colors hover:bg-accent ${
      active ? 'bg-accent font-medium text-foreground' : 'text-muted-foreground'
    }`

  return (
    <>
      <Popover open={pickerOpen} onOpenChange={setPickerOpen}>
        <PopoverTrigger asChild>
          <Button
            variant="outline"
            className="h-9 max-w-56 gap-1.5 px-3"
            title={t('sidebar.spaces.label')}
          >
            <Layers className="h-4 w-4 text-primary" />
            <span className="text-sm text-muted-foreground">
              {t('sidebar.spaces.label')}
            </span>
            <span className="max-w-32 truncate text-sm font-medium">
              {currentSpace ?? t('sidebar.spaces.all')}
            </span>
            <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
          </Button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-60 p-2">
          <Input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t('sidebar.spaces.search')}
            className="mb-1 h-7 px-2 text-xs"
          />
          <div ref={listRef} className="max-h-56 overflow-y-auto">
            <button
              type="button"
              onClick={() => selectSpace(null)}
              className={spaceRowClass(currentSpace === null)}
            >
              {t('sidebar.spaces.all')}
            </button>
            {filteredSpaces.map((space) => (
              <button
                key={space}
                type="button"
                data-active={space === currentSpace}
                onClick={() => selectSpace(space)}
                className={`flex w-full items-center truncate rounded-md px-2 py-1.5 text-sm transition-colors hover:bg-accent ${
                  space === currentSpace
                    ? 'bg-accent font-medium text-foreground'
                    : 'text-muted-foreground'
                }`}
              >
                {space}
              </button>
            ))}
            {filteredSpaces.length === 0 && (
              <div className="px-2 py-2 text-xs text-muted-foreground">
                {t('sidebar.spaces.empty')}
              </div>
            )}
          </div>
          <div className="mt-1 border-t pt-1">
            <Button
              variant="ghost"
              size="sm"
              className="w-full justify-start text-xs text-muted-foreground"
              onClick={() => {
                setPickerOpen(false)
                openCreateDialog()
              }}
            >
              <Plus className="h-3.5 w-3.5" />
              {t('sidebar.spaces.create')}
            </Button>
          </div>
        </PopoverContent>
      </Popover>

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>{t('sidebar.spaces.createTitle')}</DialogTitle>
            <DialogDescription>
              {t('sidebar.spaces.createDescription')}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label>{t('sidebar.spaces.targetCluster')}</Label>
              <Select value={cluster} onValueChange={setCluster}>
                <SelectTrigger className="w-full">
                  <SelectValue
                    placeholder={t('sidebar.spaces.targetCluster')}
                  />
                </SelectTrigger>
                <SelectContent>
                  {availableClusters.map((candidate) => (
                    <SelectItem key={candidate.name} value={candidate.name}>
                      {candidate.name}
                      {candidate.version ? ` · ${candidate.version}` : ''}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Input
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder={t('sidebar.spaces.namePlaceholder')}
              onKeyDown={(event) => {
                if (event.key === 'Enter') {
                  void handleCreate()
                }
              }}
              autoFocus
            />
          </div>
          {nameError && <p className="text-xs text-destructive">{nameError}</p>}
          <DialogFooter>
            <Button variant="outline" onClick={resetCreateDialog}>
              {t('common.actions.cancel')}
            </Button>
            <Button
              onClick={() => void handleCreate()}
              disabled={isCreating || !cluster}
            >
              {t('common.actions.create')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
