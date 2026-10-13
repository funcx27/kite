import { useState } from 'react'
import {
  ApplicationApiError,
  fetchApplicationConfig,
  fetchApplicationRevision,
  rollbackApplicationConfig,
  useApplicationConfig,
  useApplicationRevisions,
} from '@/application/api'
import type { Application } from '@/application/types'
import { useQueryClient } from '@tanstack/react-query'
import * as yaml from 'js-yaml'
import { Eye, Undo2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { formatDate, translateError } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { YamlDiffViewer } from '@/components/yaml-diff-viewer'

interface HistoryDialogProps {
  app: Application
  cluster: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

const revisionSourceKeys: Record<string, string> = {
  render: 'render',
  wizard: 'wizard',
  'deploy-image': 'deployImage',
  sync: 'sync',
  resolve: 'resolve',
  'pull-credential': 'pullCredential',
  copy: 'copy',
  yaml: 'yaml',
  rollback: 'rollback',
}

// History panel (design §7/§11): the config's revision chain with masked
// markers and any two revisions diffed. Rollback opens the target revision's
// diff against the current config (title names both revs) — confirming inside
// that dialog persists the rollback revision and publishes it in one request.
export function HistoryDialog({
  app,
  cluster,
  open,
  onOpenChange,
}: HistoryDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { data } = useApplicationRevisions(app.namespace, app.name, cluster, {
    disable: !open,
  })
  // latestRev gates the rollback buttons: rolling back to the current
  // revision is a no-op and stays disabled.
  const { data: config } = useApplicationConfig(
    app.namespace,
    app.name,
    cluster
  )
  const latestRev = config?.latestRev ?? 0
  const [selected, setSelected] = useState<number[]>([])
  const [diff, setDiff] = useState<{
    open: boolean
    original: string
    modified: string
    title: string
  }>({ open: false, original: '', modified: '', title: '' })
  const [rollingBack, setRollingBack] = useState(false)
  const [rollbackDiff, setRollbackDiff] = useState<{
    open: boolean
    rev: number
    fromRev: number
    original: string
    modified: string
  }>({ open: false, rev: 0, fromRev: 0, original: '', modified: '' })
  const [view, setView] = useState<{
    open: boolean
    content: string
    title: string
  }>({ open: false, content: '', title: '' })

  const revisions = data?.revisions ?? []

  const toggleSelect = (rev: number) => {
    setSelected((prev) => {
      if (prev.includes(rev)) {
        return prev.filter((item) => item !== rev)
      }
      const next = [...prev, rev]
      return next.length > 2 ? [next[1], next[2]] : next
    })
  }

  const compareSelected = async () => {
    if (selected.length !== 2) return
    const [a, b] = [...selected].sort((x, y) => x - y)
    try {
      const [older, newer] = await Promise.all([
        fetchApplicationRevision(app.namespace, app.name, cluster, a),
        fetchApplicationRevision(app.namespace, app.name, cluster, b),
      ])
      setDiff({
        open: true,
        original: yaml.dump(older.manifests),
        modified: yaml.dump(newer.manifests),
        title: t('applications.history.diffTitle', { from: a, to: b }),
      })
    } catch (err) {
      toast.error(translateError(err, t))
    }
  }

  // Full manifests of one revision in a read-only viewer; Secret values are
  // already masked (*** — see the row's redacted badge).
  const viewRevision = async (rev: number) => {
    try {
      const revision = await fetchApplicationRevision(
        app.namespace,
        app.name,
        cluster,
        rev
      )
      setView({
        open: true,
        content: yaml.dump(revision.manifests),
        title: t('applications.history.viewTitle', { rev }),
      })
    } catch (err) {
      toast.error(translateError(err, t))
    }
  }

  // Opens the target revision's diff against the current config; the actual
  // rollback is confirmed inside that dialog (one request = persist + publish).
  const openRollbackDiff = async (rev: number) => {
    setRollingBack(true)
    try {
      const [config, revision] = await Promise.all([
        fetchApplicationConfig(app.namespace, app.name, cluster),
        fetchApplicationRevision(app.namespace, app.name, cluster, rev),
      ])
      setRollbackDiff({
        open: true,
        rev,
        fromRev: config.latestRev,
        original: yaml.dump(config.manifests),
        modified: yaml.dump(revision.manifests),
      })
    } catch (err) {
      toast.error(translateError(err, t))
    } finally {
      setRollingBack(false)
    }
  }

  const confirmRollback = async () => {
    const rev = rollbackDiff.rev
    setRollingBack(true)
    try {
      const result = await rollbackApplicationConfig(
        app.namespace,
        app.name,
        cluster,
        {
          rev,
          deploy: true,
        }
      )
      await queryClient.invalidateQueries({ queryKey: ['applications'] })
      const failed = (result.results ?? []).filter((r) => !r.success).length
      if (result.success === false) {
        toast.warning(t('applications.history.rollbackPartial', { failed }))
      } else {
        toast.success(t('applications.history.rolledBack', { rev }))
      }
      setRollbackDiff((prev) => ({ ...prev, open: false }))
      onOpenChange(false)
    } catch (err) {
      if (err instanceof ApplicationApiError && err.status === 409) {
        toast.error(t('applications.history.rollbackConflict'))
        setRollbackDiff((prev) => ({ ...prev, open: false }))
      } else {
        toast.error(translateError(err, t))
      }
    } finally {
      setRollingBack(false)
    }
  }

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="flex max-h-[80vh] sm:max-w-2xl flex-col overflow-hidden">
          <DialogHeader>
            <DialogTitle>
              {t('applications.history.title', {
                namespace: app.namespace,
                name: app.name,
                cluster,
              })}
            </DialogTitle>
            <DialogDescription className="sr-only">
              {t('applications.history.title', { cluster })}
            </DialogDescription>
          </DialogHeader>

          <div className="min-h-0 flex-1 overflow-y-auto">
            <div className="rounded-md border">
              <table className="w-full text-sm">
                <thead className="bg-muted/60">
                  <tr className="border-b text-left text-xs text-muted-foreground">
                    <th className="w-10 px-3 py-2"></th>
                    <th className="px-3 py-2">
                      {t('applications.history.rev')}
                    </th>
                    <th className="px-3 py-2">
                      {t('applications.history.sourceLabel')}
                    </th>
                    <th className="px-3 py-2">
                      {t('applications.history.operator')}
                    </th>
                    <th className="px-3 py-2">
                      {t('applications.history.time')}
                    </th>
                    <th className="px-3 py-2 text-right">
                      {t('common.fields.actions')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {revisions.map((revision) => (
                    <tr
                      key={revision.rev}
                      className="cursor-pointer border-b last:border-b-0 hover:bg-muted/40"
                      onClick={() => toggleSelect(revision.rev)}
                    >
                      <td className="px-3 py-2">
                        <input
                          type="checkbox"
                          checked={selected.includes(revision.rev)}
                          onChange={() => toggleSelect(revision.rev)}
                          onClick={(event) => event.stopPropagation()}
                        />
                      </td>
                      <td className="px-3 py-2 font-mono">#{revision.rev}</td>
                      <td className="px-3 py-2">
                        <span className="inline-flex items-center gap-1.5">
                          {revision.source === 'rollback' && revision.refRev ? (
                            <span className="font-mono text-xs">
                              {t('applications.history.sourceRollbackFrom', {
                                rev: revision.refRev,
                              })}
                            </span>
                          ) : (
                            t(
                              `applications.history.source.${revisionSourceKeys[revision.source] ?? 'render'}`
                            )
                          )}
                          {revision.redacted && (
                            <Badge
                              variant="outline"
                              className="text-[10px] text-amber-600"
                            >
                              {t('applications.history.redacted')}
                            </Badge>
                          )}
                        </span>
                      </td>
                      <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
                        {revision.operatorName ||
                          (revision.operatorId > 0
                            ? `#${revision.operatorId}`
                            : '—')}
                      </td>
                      <td className="px-3 py-2 text-xs text-muted-foreground">
                        {formatDate(revision.createdAt)}
                      </td>
                      <td className="px-3 py-2 text-right">
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8"
                            title={t('applications.history.viewYaml')}
                            aria-label={t('applications.history.viewYaml')}
                            onClick={(event) => {
                              event.stopPropagation()
                              void viewRevision(revision.rev)
                            }}
                          >
                            <Eye className="h-4 w-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8"
                            disabled={rollingBack || revision.rev === latestRev}
                            title={
                              revision.rev === latestRev
                                ? t('applications.history.rollbackSelfHint')
                                : t('applications.history.rollbackHint')
                            }
                            aria-label={t('applications.history.rollback')}
                            onClick={(event) => {
                              event.stopPropagation()
                              void openRollbackDiff(revision.rev)
                            }}
                          >
                            <Undo2 className="h-4 w-4" />
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))}
                  {revisions.length === 0 && (
                    <tr>
                      <td
                        colSpan={6}
                        className="px-3 py-8 text-center text-sm text-muted-foreground"
                      >
                        {t('applications.history.empty')}
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
            <p className="mt-2 text-xs text-muted-foreground">
              {t('applications.history.selectHint')}
            </p>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {t('common.actions.close')}
            </Button>
            <Button
              disabled={selected.length !== 2}
              onClick={() => void compareSelected()}
            >
              {t('applications.history.compare')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <YamlDiffViewer
        open={diff.open}
        onOpenChange={(nextOpen) =>
          setDiff((prev) => ({ ...prev, open: nextOpen }))
        }
        original={diff.original}
        modified={diff.modified}
        title={diff.title}
      />

      <YamlDiffViewer
        viewOnly
        raw
        open={view.open}
        onOpenChange={(nextOpen) =>
          setView((prev) => ({ ...prev, open: nextOpen }))
        }
        content={view.content}
        title={view.title}
      />

      {rollbackDiff.open && rollbackDiff.original === rollbackDiff.modified ? (
        // Nothing differs (e.g. a stale click after the config advanced):
        // a full-expanded identical diff helps nobody — say so instead.
        <Dialog
          open={rollbackDiff.open}
          onOpenChange={(nextOpen) =>
            setRollbackDiff((prev) => ({ ...prev, open: nextOpen }))
          }
        >
          <DialogContent className="max-w-md">
            <DialogHeader>
              <DialogTitle>
                {t('applications.history.diffTitle', {
                  from: rollbackDiff.fromRev,
                  to: rollbackDiff.rev,
                })}
              </DialogTitle>
              <DialogDescription className="sr-only">
                {t('applications.history.rollback')}
              </DialogDescription>
            </DialogHeader>
            <p className="text-sm text-muted-foreground">
              {t('applications.history.rollbackIdentical')}
            </p>
            <div className="flex justify-end">
              <Button
                variant="outline"
                onClick={() =>
                  setRollbackDiff((prev) => ({ ...prev, open: false }))
                }
              >
                {t('common.actions.close')}
              </Button>
            </div>
          </DialogContent>
        </Dialog>
      ) : (
        <YamlDiffViewer
          open={rollbackDiff.open}
          onOpenChange={(nextOpen) =>
            setRollbackDiff((prev) => ({ ...prev, open: nextOpen }))
          }
          original={rollbackDiff.original}
          modified={rollbackDiff.modified}
          title={t('applications.history.diffTitle', {
            from: rollbackDiff.fromRev,
            to: rollbackDiff.rev,
          })}
          hideUnchanged={true}
          height={560}
          action={
            <Button
              size="sm"
              disabled={rollingBack}
              onClick={() => void confirmRollback()}
            >
              {rollingBack
                ? t('applications.history.rollingBack')
                : t('applications.history.rollbackConfirm')}
            </Button>
          }
        />
      )}
    </>
  )
}
