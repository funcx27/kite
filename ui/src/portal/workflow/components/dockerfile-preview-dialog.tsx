// Read-only preview of the dockerfile a build node's path field points at,
// fetched through the buildfleet preview endpoint: the ref resolves
// server-side and the content is read at the commit the fetch actually
// landed on. Display and copy only — the path field stays a live reference
// tracking the latest commit, so the dialog never writes back into the form.
import { useEffect } from 'react'
import { IconCopy, IconRefresh } from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

import { useDockerfilePreviewMutation } from '../api'
import type { DockerfilePreviewRequest } from '../api'

export function DockerfilePreviewDialog({
  open,
  onOpenChange,
  request,
  title,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  request: DockerfilePreviewRequest
  title: string
}) {
  const { t } = useTranslation()
  const preview = useDockerfilePreviewMutation()
  // Refetch on every open (and manual retry): the path tracks the latest
  // commit, so a previously fetched answer may already be stale.
  useEffect(() => {
    if (open) preview.mutate(request)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const data = preview.data
  const shortCommit = data?.commit ? data.commit.slice(0, 7) : ''

  const copyContent = async () => {
    if (!data?.content) return
    await navigator.clipboard.writeText(data.content)
    toast.success(t('common.messages.copied'))
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="break-all">{title}</DialogTitle>
        </DialogHeader>
        <div className="min-h-0 flex-1 overflow-auto">
          {preview.isPending ? (
            <p className="py-10 text-center text-sm text-muted-foreground">
              {t('workflow.dockerfilePreview.loading')}
            </p>
          ) : preview.isError ? (
            <div className="space-y-3 py-8 text-center">
              <p className="break-all px-4 text-sm text-destructive">
                {preview.error.message}
              </p>
              <Button
                variant="outline"
                size="sm"
                onClick={() => preview.mutate(request)}
              >
                <IconRefresh className="mr-1.5 h-4 w-4" />
                {t('workflow.dockerfilePreview.retry')}
              </Button>
            </div>
          ) : data && !data.exists ? (
            <p className="py-10 text-center text-sm text-muted-foreground">
              {shortCommit
                ? t('workflow.dockerfilePreview.notFoundAt', {
                    commit: shortCommit,
                  })
                : t('workflow.dockerfilePreview.notFound')}
            </p>
          ) : (
            <pre className="rounded-md border bg-muted/40 p-3 font-mono text-xs leading-5">
              {data?.content}
            </pre>
          )}
        </div>
        <DialogFooter className="items-center sm:justify-between">
          <div className="min-w-0 truncate text-xs text-muted-foreground">
            {data?.exists && shortCommit
              ? t('workflow.dockerfilePreview.atCommit', {
                  commit: shortCommit,
                })
              : ''}
          </div>
          <div className="flex gap-2">
            {data?.exists && data.content && (
              <Button variant="outline" onClick={copyContent}>
                <IconCopy className="mr-1.5 h-4 w-4" />
                {t('common.actions.copy')}
              </Button>
            )}
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {t('common.actions.close')}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
