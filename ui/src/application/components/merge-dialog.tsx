import { useEffect, useMemo, useState } from 'react'
import {
  mergeApplicationConfig,
  useApplicationMergePreview,
} from '@/application/api'
import {
  MERGE_DOC_ROOT_PATH,
  type Application,
  type ApplicationMergeChoice,
} from '@/application/types'
import { useQueryClient } from '@tanstack/react-query'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { translateError } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

interface MergeDialogProps {
  app: Application
  cluster: string
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Called after the merge was written to the DB (Source=merge). */
  onMerged: () => void
}

// Mirrors syncops.ScopedPath: "<docKey>:<path>" with docKey
// "<kind>/<namespace>/<name>" (cluster.go).
function scopedPath(
  kind: string,
  namespace: string | undefined,
  name: string,
  path: string
) {
  return `${kind}/${namespace ?? ''}/${name}:${path}`
}

// Conflict handling (design §4/§11): three-way merge preview — auto-adopted
// items carry their source and can be rolled back to the baseline (knowing
// override), collisions need an ours/theirs pick each before confirming.
export function MergeDialog({
  app,
  cluster,
  open,
  onOpenChange,
  onMerged,
}: MergeDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { data: preview } = useApplicationMergePreview(app.name, cluster, {
    disable: !open,
  })
  const [rollbacks, setRollbacks] = useState<Set<string>>(new Set())
  const [choices, setChoices] = useState<
    Record<string, ApplicationMergeChoice>
  >({})
  // Auto-adopted items show only the path until expanded: values (base →
  // cluster) can be long and would break the row layout.
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [merging, setMerging] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (open) {
      setRollbacks(new Set())
      setChoices({})
      setExpanded(new Set())
      setError('')
    }
  }, [open])

  const autoItems = useMemo(() => {
    // AutoAdopted lists the paths taken from the cluster side (merge.go),
    // with the baseline value replaced and the adopted cluster value.
    const items: Array<{
      scoped: string
      path: string
      base?: unknown
      theirs?: unknown
    }> = []
    for (const doc of preview?.docs ?? []) {
      const docLabel = `${doc.kind}/${doc.name}`
      for (const adopted of doc.autoAdopted ?? []) {
        items.push({
          scoped: scopedPath(doc.kind, doc.namespace, doc.name, adopted.path),
          path:
            adopted.path === MERGE_DOC_ROOT_PATH
              ? docLabel
              : `${docLabel} · ${adopted.path}`,
          base: adopted.base,
          theirs: adopted.theirs,
        })
      }
    }
    return items
  }, [preview])

  const collisions = useMemo(
    () =>
      (preview?.docs ?? []).flatMap((doc) =>
        (doc.collisions ?? []).map((collision) => ({
          doc: `${doc.kind}/${doc.name}`,
          scoped: scopedPath(doc.kind, doc.namespace, doc.name, collision.path),
          ...collision,
        }))
      ),
    [preview]
  )

  // The outcome is derived from the current selections, not the load-time
  // preview: rolled-back adoptions and collisions kept on the DB side no
  // longer match the cluster, so a publish is needed; everything adopted
  // matches the cluster exactly and the config closes as synced.
  const publishRequired =
    rollbacks.size > 0 ||
    collisions.some((collision) => choices[collision.scoped] !== 'theirs')

  const allCollisionsResolved =
    collisions.length === 0 ||
    collisions.every((collision) => choices[collision.scoped])

  const handleMerge = async () => {
    setMerging(true)
    setError('')
    try {
      const choicesPayload: Record<string, ApplicationMergeChoice> = {}
      for (const collision of collisions) {
        const choice = choices[collision.scoped]
        if (choice) {
          choicesPayload[collision.scoped] = choice
        }
      }
      const result = await mergeApplicationConfig(app.name, cluster, {
        choices: choicesPayload,
        rollbacks: Array.from(rollbacks),
      })
      await queryClient.invalidateQueries({ queryKey: ['applications'] })
      onOpenChange(false)
      if (result.publishRequired === false) {
        // Pure adoption: the backend re-anchored, the config is synced.
        toast.success(t('applications.merge.mergedNoPublish', { cluster }))
        return
      }
      toast.success(t('applications.merge.merged', { cluster }))
      onMerged()
    } catch (err) {
      setError(translateError(err, t))
    } finally {
      setMerging(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85vh] sm:max-w-xl flex-col overflow-hidden">
        <DialogHeader>
          <DialogTitle>
            {t('applications.merge.title', { cluster })}
          </DialogTitle>
          <DialogDescription className="sr-only">
            {t('applications.merge.title', { cluster })}
          </DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="flex items-start gap-2 rounded-lg bg-muted/50 px-3 py-2.5 text-xs text-muted-foreground">
            <span>ℹ</span>
            <span>{t('applications.merge.hint')}</span>
          </div>

          <div className="mt-4 text-sm font-medium">
            {t('applications.merge.autoSection')}
          </div>
          <div className="mt-2 space-y-1.5">
            {autoItems.length === 0 && (
              <div className="rounded-lg border px-3 py-2 text-xs text-muted-foreground">
                {t('applications.merge.noAutoItems')}
              </div>
            )}
            {autoItems.map((item) => {
              const rolledBack = rollbacks.has(item.scoped)
              const hasValues =
                item.base !== undefined || item.theirs !== undefined
              const isOpen = expanded.has(item.scoped)
              return (
                <div
                  key={item.scoped}
                  className={
                    rolledBack
                      ? 'flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2 opacity-50'
                      : 'flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2'
                  }
                >
                  <span className="inline-flex items-center rounded-full border border-amber-200 bg-amber-50 px-2 py-0.5 text-xs font-medium text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-400">
                    {t('applications.merge.fromCluster')}
                  </span>
                  {hasValues ? (
                    <button
                      type="button"
                      className="flex min-w-0 flex-1 cursor-pointer items-center gap-1 text-left font-mono text-xs text-muted-foreground hover:text-foreground"
                      onClick={() =>
                        setExpanded((prev) => {
                          const next = new Set(prev)
                          if (next.has(item.scoped)) {
                            next.delete(item.scoped)
                          } else {
                            next.add(item.scoped)
                          }
                          return next
                        })
                      }
                    >
                      {isOpen ? (
                        <ChevronDown className="h-3 w-3 shrink-0" />
                      ) : (
                        <ChevronRight className="h-3 w-3 shrink-0" />
                      )}
                      <span className="break-all">{item.path}</span>
                    </button>
                  ) : (
                    <span className="min-w-0 flex-1 break-all font-mono text-xs text-muted-foreground">
                      {item.path}
                    </span>
                  )}
                  {hasValues && isOpen && (
                    <div className="w-full space-y-1 font-mono text-xs">
                      <div className="flex items-start gap-2">
                        <span className="shrink-0 rounded bg-muted px-1.5 py-0.5">
                          {t('applications.merge.baseline')}
                        </span>
                        <span className="min-w-0 whitespace-pre-wrap break-all text-muted-foreground">
                          {formatValue(item.base)}
                        </span>
                      </div>
                      <div className="flex items-start gap-2">
                        <span className="shrink-0 rounded bg-amber-100 px-1.5 py-0.5 text-amber-700 dark:bg-amber-950 dark:text-amber-400">
                          {t('applications.merge.theirs')}
                        </span>
                        <span className="min-w-0 whitespace-pre-wrap break-all">
                          {formatValue(item.theirs)}
                        </span>
                      </div>
                    </div>
                  )}
                  <button
                    type="button"
                    className="ml-auto cursor-pointer text-xs text-muted-foreground transition-colors hover:text-destructive"
                    onClick={() =>
                      setRollbacks((prev) => {
                        const next = new Set(prev)
                        if (next.has(item.scoped)) {
                          next.delete(item.scoped)
                        } else {
                          next.add(item.scoped)
                        }
                        return next
                      })
                    }
                  >
                    {rolledBack
                      ? t('applications.merge.undoRollback')
                      : t('applications.merge.rollback')}
                  </button>
                </div>
              )
            })}
          </div>

          {collisions.length > 0 && (
            <>
              <div className="mt-4 text-sm font-medium">
                {t('applications.merge.collisionSection')}
              </div>
              <div className="mt-2 space-y-2">
                {collisions.map((collision) => (
                  <div
                    key={collision.scoped}
                    className="rounded-lg border border-amber-200 bg-amber-50/40 px-3 py-2 dark:border-amber-800 dark:bg-amber-950/40"
                  >
                    <div className="font-mono text-xs text-muted-foreground">
                      {collision.doc} · {collision.path}
                    </div>
                    <div className="mt-1.5 flex flex-wrap items-center gap-2 text-xs">
                      <span className="text-muted-foreground">
                        {t('applications.merge.baseline')}{' '}
                        <span className="font-mono">
                          {formatValue(collision.base)}
                        </span>
                      </span>
                      <span className="text-muted-foreground">
                        {t('applications.merge.ours')}{' '}
                        <span className="font-semibold">
                          {formatValue(collision.ours)}
                        </span>
                      </span>
                      <span className="font-medium text-amber-700 dark:text-amber-400">
                        {t('applications.merge.theirs')}{' '}
                        <span className="font-semibold">
                          {formatValue(collision.theirs)}
                        </span>
                      </span>
                      <span className="ml-auto flex gap-1.5">
                        <Button
                          size="sm"
                          variant={
                            choices[collision.scoped] === 'ours'
                              ? 'default'
                              : 'outline'
                          }
                          onClick={() =>
                            setChoices((prev) => ({
                              ...prev,
                              [collision.scoped]: 'ours',
                            }))
                          }
                        >
                          {t('applications.merge.keepOurs')}
                        </Button>
                        <Button
                          size="sm"
                          variant={
                            choices[collision.scoped] === 'theirs'
                              ? 'default'
                              : 'outline'
                          }
                          onClick={() =>
                            setChoices((prev) => ({
                              ...prev,
                              [collision.scoped]: 'theirs',
                            }))
                          }
                        >
                          {t('applications.merge.takeTheirs')}
                        </Button>
                      </span>
                    </div>
                  </div>
                ))}
              </div>
            </>
          )}

          {error && <p className="mt-3 text-xs text-destructive">{error}</p>}
        </div>

        {preview && (
          <div
            className={`mx-5 mb-1 flex items-start gap-2 rounded-lg px-3 py-2.5 text-xs ${
              publishRequired
                ? 'bg-sky-50 text-sky-800 dark:bg-sky-950/40 dark:text-sky-300'
                : 'bg-emerald-50 text-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-300'
            }`}
          >
            <span>{publishRequired ? '→' : '✓'}</span>
            <span>
              {publishRequired
                ? t('applications.merge.outcomePublish')
                : t('applications.merge.outcomeSync')}
            </span>
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.actions.cancel')}
          </Button>
          <Button
            disabled={!preview || !allCollisionsResolved || merging}
            onClick={() => void handleMerge()}
          >
            {publishRequired
              ? t('applications.merge.confirm')
              : t('applications.merge.confirmSync')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function formatValue(value: unknown): string {
  if (value === undefined || value === null) return '—'
  if (typeof value === 'string') return value
  return JSON.stringify(value)
}
