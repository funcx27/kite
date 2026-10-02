import { useMemo, useState } from 'react'
import { ApplicationConfigFile } from '@/application/types'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

interface ConfigFilesEditorProps {
  appName: string
  mountPoints: string[]
  configFiles: ApplicationConfigFile[]
  onChange: (
    mountPoints: string[],
    configFiles: ApplicationConfigFile[]
  ) => void
}

// Wizard step 3 (design §9): a set of mount points plus config files that
// must belong to one of them (dropdown, no free-text path). Files render
// grouped by mount point; clicking a file name opens the floating editor
// (fullscreen capable) with the sensitive marker.
export function ConfigFilesEditor({
  appName,
  mountPoints,
  configFiles,
  onChange,
}: ConfigFilesEditorProps) {
  const { t } = useTranslation()
  const [points, setPoints] = useState(mountPoints)
  const [files, setFiles] = useState(configFiles)
  const [addingPoint, setAddingPoint] = useState(false)
  const [pointInput, setPointInput] = useState('')
  const [pointError, setPointError] = useState('')
  const [editingIndex, setEditingIndex] = useState<number | null>(null)
  const [fullscreen, setFullscreen] = useState(false)

  const commit = (nextPoints: string[], nextFiles: ApplicationConfigFile[]) => {
    setPoints(nextPoints)
    setFiles(nextFiles)
    onChange(nextPoints, nextFiles)
  }

  const editing = editingIndex !== null ? files[editingIndex] : undefined

  const addMountPoint = () => {
    const value = pointInput.trim().replace(/\/+$/, '')
    if (!value.startsWith('/')) {
      setPointError(t('applications.wizard.mountPointInvalid'))
      return
    }
    if (points.includes(value)) {
      setPointError(t('applications.wizard.mountPointExists'))
      return
    }
    commit([...points, value], files)
    setPointInput('')
    setPointError('')
    setAddingPoint(false)
  }

  const removeMountPoint = (point: string) => {
    if (files.some((file) => file.mountPath === point)) return
    commit(
      points.filter((p) => p !== point),
      files
    )
  }

  const addFile = () => {
    if (points.length === 0) {
      setAddingPoint(true)
      return
    }
    const base = 'file'
    let name = `${base}.conf`
    for (let i = files.length + 1; files.some((f) => f.name === name); i++) {
      name = `${base}${i}.conf`
    }
    const next = [
      ...files,
      { name, mountPath: points[0], content: '', secret: false },
    ]
    commit(points, next)
    setEditingIndex(next.length - 1)
    setFullscreen(false)
  }

  const removeFile = (index: number) => {
    commit(
      points,
      files.filter((_, i) => i !== index)
    )
  }

  const updateEditing = (updates: Partial<ApplicationConfigFile>) => {
    if (editingIndex === null) return
    commit(
      points,
      files.map((file, i) =>
        i === editingIndex ? { ...file, ...updates } : file
      )
    )
  }

  const grouped = useMemo(() => {
    return points
      .map((point) => ({
        point,
        files: files
          .map((file, index) => ({ file, index }))
          .filter(({ file }) => file.mountPath === point),
      }))
      .filter((group) => group.files.length > 0)
  }, [points, files])

  const editingStat = useMemo(() => {
    if (!editing) return ''
    const lines = editing.content ? editing.content.split('\n').length : 0
    return t('applications.wizard.fileStat', {
      lines,
      chars: editing.content.length,
    })
  }, [editing, t])

  return (
    <div className="rounded-lg border p-4">
      <div className="text-sm font-medium">
        {t('applications.wizard.configFiles')}
        <span className="ml-1.5 text-xs font-normal text-muted-foreground">
          {t('common.fields.optional', 'Optional')}
        </span>
      </div>
      <p className="mt-1 text-xs text-muted-foreground">
        {t('applications.wizard.configFilesHint')}
      </p>

      <div className="mt-3 rounded-lg border p-3">
        <div className="text-xs font-medium text-muted-foreground">
          {t('applications.wizard.mountPoints')}
        </div>
        <div className="mt-2 flex flex-wrap gap-1.5">
          {points.map((point) => {
            const count = files.filter((f) => f.mountPath === point).length
            return (
              <span
                key={point}
                className="inline-flex items-center gap-1 rounded-full border bg-muted/40 px-2.5 py-0.5 font-mono text-xs text-muted-foreground"
              >
                {point} · {t('applications.wizard.fileCount', { count })}
                {count === 0 && (
                  <button
                    type="button"
                    className="text-muted-foreground/60 hover:text-destructive"
                    title={t('applications.wizard.removeMountPoint')}
                    onClick={() => removeMountPoint(point)}
                  >
                    ×
                  </button>
                )}
              </span>
            )
          })}
          {points.length === 0 && (
            <span className="text-xs text-muted-foreground">
              {t('applications.wizard.noMountPoints')}
            </span>
          )}
        </div>
        {addingPoint && (
          <div className="mt-2 flex items-center gap-2">
            <Input
              value={pointInput}
              onChange={(event) => setPointInput(event.target.value)}
              placeholder="/etc/xxx"
              className="h-8 max-w-56 py-0 font-mono text-xs"
              onKeyDown={(event) => {
                if (event.key === 'Enter') addMountPoint()
                if (event.key === 'Escape') setAddingPoint(false)
              }}
              autoFocus
            />
            <Button type="button" size="sm" onClick={addMountPoint}>
              {t('common.actions.add')}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={() => {
                setAddingPoint(false)
                setPointError('')
              }}
            >
              {t('common.actions.cancel')}
            </Button>
            {pointError && (
              <span className="text-xs text-destructive">{pointError}</span>
            )}
          </div>
        )}
        {!addingPoint && (
          <div className="mt-2 text-center">
            <button
              type="button"
              className="cursor-pointer text-xs text-muted-foreground transition-colors hover:text-foreground"
              onClick={() => setAddingPoint(true)}
            >
              ＋ {t('applications.wizard.addMountPoint')}
            </button>
          </div>
        )}
      </div>

      {grouped.length > 0 ? (
        <div className="mt-4 space-y-4">
          {grouped.map((group) => (
            <div key={group.point}>
              <div className="mb-1.5 flex items-center gap-2">
                <span className="rounded-full border bg-muted/40 px-2.5 py-0.5 font-mono text-xs text-muted-foreground">
                  {group.point}
                </span>
                <span className="text-xs text-muted-foreground">
                  {t('applications.wizard.fileCount', {
                    count: group.files.length,
                  })}
                </span>
              </div>
              <div className="space-y-1.5">
                {group.files.map(({ file, index }) => (
                  <div
                    key={index}
                    className="flex items-center gap-2 rounded-lg border p-3"
                  >
                    <button
                      type="button"
                      className="min-w-0 truncate font-mono text-sm hover:underline"
                      onClick={() => {
                        setEditingIndex(index)
                        setFullscreen(false)
                      }}
                    >
                      {file.name}
                    </button>
                    <span
                      className={
                        file.secret
                          ? 'inline-flex items-center rounded-full border border-amber-200 bg-amber-50 px-2 py-0.5 text-xs font-medium text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-400'
                          : 'inline-flex items-center rounded-full border bg-zinc-50 px-2 py-0.5 text-xs font-medium text-zinc-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-400'
                      }
                    >
                      {file.secret ? 'Secret' : 'ConfigMap'}
                    </span>
                    <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                      {file.content ? file.content.split('\n').length : 0}{' '}
                      {t('applications.wizard.lines')}
                    </span>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="shrink-0 px-2"
                      title={t('applications.wizard.removeFile')}
                      onClick={() => removeFile(index)}
                    >
                      ✕
                    </Button>
                  </div>
                ))}
              </div>
            </div>
          ))}
          <div className="pt-1 text-center">
            <button
              type="button"
              className="cursor-pointer text-xs text-muted-foreground transition-colors hover:text-foreground"
              onClick={addFile}
            >
              ＋ {t('applications.wizard.addFile')}
            </button>
          </div>
        </div>
      ) : (
        <button
          type="button"
          className="mt-4 w-full cursor-pointer rounded-lg border border-dashed px-4 py-8 text-center transition-colors hover:border-muted-foreground"
          onClick={addFile}
        >
          <div className="text-sm text-muted-foreground">
            ＋ {t('applications.wizard.addFile')}
          </div>
          <div className="mt-1 text-xs text-muted-foreground/70">
            {t('applications.wizard.configFilesEmptyHint')}
          </div>
        </button>
      )}

      <p className="mt-3 text-xs text-muted-foreground">
        {t('applications.wizard.configFilesAdvancedHint')}
      </p>

      <Dialog
        open={editingIndex !== null}
        onOpenChange={(open) => {
          if (!open) setEditingIndex(null)
        }}
      >
        <DialogContent
          className={
            fullscreen
              ? 'flex h-[calc(100dvh-1.5rem)] sm:max-w-none flex-col gap-3 overflow-hidden'
              : 'flex h-[80vh] sm:max-w-4xl flex-col gap-3 overflow-hidden'
          }
        >
          <DialogHeader className="sr-only">
            <DialogTitle>{t('applications.wizard.editFile')}</DialogTitle>
          </DialogHeader>
          <div className="flex flex-wrap items-center gap-2">
            <Input
              value={editing?.name ?? ''}
              onChange={(event) => updateEditing({ name: event.target.value })}
              placeholder={t('applications.wizard.fileName')}
              className="h-8 max-w-52 py-0 font-mono text-sm"
            />
            <Select
              value={editing?.mountPath ?? ''}
              onValueChange={(value) => updateEditing({ mountPath: value })}
            >
              <SelectTrigger className="h-8 max-w-52 py-0 font-mono text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {points.map((point) => (
                  <SelectItem key={point} value={point}>
                    {point}
                  </SelectItem>
                ))}
                {editing && !points.includes(editing.mountPath) && (
                  <SelectItem value={editing.mountPath}>
                    {editing.mountPath}
                  </SelectItem>
                )}
              </SelectContent>
            </Select>
            <div className="ml-auto flex items-center gap-1.5">
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => setFullscreen((prev) => !prev)}
              >
                {fullscreen
                  ? t('applications.wizard.exitFullscreen')
                  : t('applications.wizard.fullscreen')}
              </Button>
              <Button
                type="button"
                size="sm"
                onClick={() => setEditingIndex(null)}
              >
                {t('common.actions.done')}
              </Button>
            </div>
          </div>
          <textarea
            value={editing?.content ?? ''}
            onChange={(event) => updateEditing({ content: event.target.value })}
            placeholder={t('applications.wizard.fileContentPlaceholder')}
            className="min-h-0 flex-1 resize-none rounded-md border bg-zinc-950 p-4 font-mono text-xs leading-5 text-zinc-200 outline-none dark:bg-zinc-950"
          />
          <div className="flex items-center gap-3">
            <label className="flex cursor-pointer items-center gap-1.5 text-xs text-muted-foreground">
              <input
                type="checkbox"
                checked={editing?.secret ?? false}
                onChange={(event) =>
                  updateEditing({ secret: event.target.checked })
                }
                className="accent-primary"
              />
              {t('applications.wizard.secretMarker', { app: appName })}
            </label>
            <span className="ml-auto text-xs text-muted-foreground">
              {editingStat}
            </span>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}
