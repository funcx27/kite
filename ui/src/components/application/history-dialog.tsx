import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import * as yaml from 'js-yaml'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import type { Application } from '@/types/application'
import {
  fetchApplicationRevision,
  rollbackApplicationConfig,
  useApplicationRevisions,
} from '@/lib/api/applications'
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

import { YamlDiffViewer } from '../yaml-diff-viewer'

interface HistoryDialogProps {
  app: Application
  cluster: string
  open: boolean
  onOpenChange: (open: boolean) => void
  /** After a rollback: the pending config should be published. */
  onRolledBack: () => void
}

const revisionSourceKeys: Record<string, string> = {
  render: 'render',
  wizard: 'wizard',
  'deploy-image': 'deployImage',
  sync: 'sync',
  merge: 'merge',
  copy: 'copy',
  yaml: 'yaml',
  rollback: 'rollback',
}

// History panel (design §7/§11): the config's revision chain with masked
// markers, any two revisions diffed, and rollback that only writes the
// chosen manifests back as pending — the cluster is touched by the
// subsequent conflict-gated publish only.
export function HistoryDialog({
  app,
  cluster,
  open,
  onOpenChange,
  onRolledBack,
}: HistoryDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { data } = useApplicationRevisions(app.name, cluster, {
    disable: !open,
  })
  const [selected, setSelected] = useState<number[]>([])
  const [diff, setDiff] = useState<{
    open: boolean
    original: string
    modified: string
    title: string
  }>({ open: false, original: '', modified: '', title: '' })
  const [rollingBack, setRollingBack] = useState(false)

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
        fetchApplicationRevision(app.name, cluster, a),
        fetchApplicationRevision(app.name, cluster, b),
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

  const rollback = async (rev: number) => {
    setRollingBack(true)
    try {
      await rollbackApplicationConfig(app.name, cluster, rev)
      await queryClient.invalidateQueries({ queryKey: ['applications'] })
      toast.success(t('applications.history.rolledBack', { rev }))
      onOpenChange(false)
      onRolledBack()
    } catch (err) {
      toast.error(translateError(err, t))
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
                          {t(
                            `applications.history.source.${revisionSourceKeys[revision.source] ?? 'render'}`
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
                        {revision.operatorId > 0
                          ? `#${revision.operatorId}`
                          : '—'}
                      </td>
                      <td className="px-3 py-2 text-xs text-muted-foreground">
                        {formatDate(revision.createdAt)}
                      </td>
                      <td className="px-3 py-2 text-right">
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={rollingBack}
                          onClick={(event) => {
                            event.stopPropagation()
                            void rollback(revision.rev)
                          }}
                        >
                          {t('applications.history.rollback')}
                        </Button>
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
    </>
  )
}
