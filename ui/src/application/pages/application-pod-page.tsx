import { useEffect, useMemo, useRef, useState } from 'react'
import {
  forceDeleteApplicationPod,
  useApplication,
  useApplicationResources,
} from '@/application/api'
import { IconChevronDown, IconTrash } from '@tabler/icons-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import * as yaml from 'js-yaml'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'

import { fetchResource } from '@/lib/api'
import { translateError } from '@/lib/utils'
import { usePageTitle } from '@/hooks/use-page-title'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { LogViewer } from '@/components/log-viewer'
import { PodFileBrowser } from '@/components/pod-file-browser'
import { PodMonitoring } from '@/components/pod-monitoring'
import { Terminal } from '@/components/terminal-content'

type View = 'monitoring' | 'terminal' | 'logs' | 'files' | 'yaml'

const VIEWS: View[] = ['monitoring', 'terminal', 'logs', 'files', 'yaml']

// Pod detail page of the application module (design §10/§11): the pod is a
// navigable entity like the application itself — its identity
// (app / cluster / pod) lives in the URL so views are shareable, and the
// tabs reuse the professional-mode components with the app's cluster
// addressed explicitly on every request.
export function ApplicationPodPage() {
  const { name = '', cluster = '', pod: podName = '' } = useParams()
  const [searchParams, setSearchParams] = useSearchParams()
  const navigate = useNavigate()
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  usePageTitle(`${podName} · ${name}`)

  const rawView = searchParams.get('view') as View | null
  const view: View = rawView && VIEWS.includes(rawView) ? rawView : 'monitoring'
  const setView = (next: string) =>
    setSearchParams(
      (prev) => {
        const nextParams = new URLSearchParams(prev)
        nextParams.set('view', next)
        return nextParams
      },
      { replace: true }
    )

  const {
    data: app,
    isLoading: appLoading,
    error: appError,
  } = useApplication(name)
  const namespace = app?.namespace ?? ''
  const { data: runtime, isLoading: runtimeLoading } = useApplicationResources(
    name,
    cluster,
    { disable: !app }
  )
  const podItem = runtime?.pods?.items.find((item) => item.name === podName)
  const podContainers = useMemo(() => podItem?.containers ?? [], [podItem])

  const podMissing = !!app && !runtimeLoading && !podItem

  const {
    data: podObject,
    isLoading: yamlLoading,
    error: yamlError,
  } = useQuery({
    queryKey: ['application-pod-yaml', cluster, namespace, podName],
    queryFn: () =>
      fetchResource<Record<string, unknown>>(
        'pods',
        podName,
        namespace,
        cluster
      ),
    enabled: view === 'yaml' && !!namespace,
    staleTime: 5000,
    retry: false,
  })

  // Two-step force-delete confirm: the button arms on the first click and
  // auto-disarms, mirroring the restart buttons on the cluster tab.
  const [deleting, setDeleting] = useState(false)
  const [armed, setArmed] = useState(false)
  const armTimer = useRef<number | undefined>(undefined)

  useEffect(() => () => window.clearTimeout(armTimer.current), [])

  const handleForceDelete = async () => {
    if (!armed) {
      window.clearTimeout(armTimer.current)
      setArmed(true)
      armTimer.current = window.setTimeout(() => setArmed(false), 3000)
      return
    }
    window.clearTimeout(armTimer.current)
    setArmed(false)
    setDeleting(true)
    try {
      await forceDeleteApplicationPod(name, cluster, podName)
      toast.success(t('applications.detail.podForceDeleted', { pod: podName }))
      await queryClient.invalidateQueries({
        queryKey: ['applications', name, 'resources', cluster],
      })
      navigate(`/applications/${name}/clusters/${cluster}`)
    } catch (err) {
      toast.error(translateError(err, t))
    } finally {
      setDeleting(false)
    }
  }

  const backTo = `/applications/${name}/clusters/${cluster}`

  // Sibling pods of the same app/cluster: the header pod name is the
  // switcher, so moving between pods never round-trips through the detail
  // page. The current tab rides along.
  const pods = runtime?.pods?.items ?? []
  const switchPod = (next: string) => {
    if (next === podName) return
    navigate({
      pathname: `/applications/${name}/clusters/${cluster}/pods/${next}`,
      search: searchParams.toString(),
    })
  }

  if (appError || (!appLoading && !app)) {
    return (
      <div className="py-10 text-center text-sm text-destructive">
        {t('applications.detail.notFound', { name })}
      </div>
    )
  }

  const podStatus = podItem?.status || podItem?.phase

  return (
    <div className="space-y-4">
      <div>
        <Link
          to={backTo}
          className="text-sm text-muted-foreground hover:text-foreground"
        >
          ← {name}
        </Link>
        <div className="mt-0.5 flex flex-wrap items-center gap-3">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                type="button"
                title={t('applications.detail.switchPod')}
                className="inline-flex items-center gap-1 font-mono text-base font-semibold hover:underline"
              >
                {podName}
                <IconChevronDown className="size-4 text-muted-foreground" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              align="start"
              className="max-h-72 overflow-y-auto"
            >
              {pods.map((item) => {
                const status = item.status || item.phase
                return (
                  <DropdownMenuItem
                    key={item.name}
                    className="gap-2 font-mono text-xs"
                    onClick={() => switchPod(item.name)}
                  >
                    <span
                      className={`h-2 w-2 shrink-0 rounded-full ${
                        status === 'Running' ? 'bg-green-500' : 'bg-amber-500'
                      }`}
                    />
                    {item.name}
                    {item.name === podName && (
                      <span className="ml-auto text-muted-foreground">✓</span>
                    )}
                  </DropdownMenuItem>
                )
              })}
            </DropdownMenuContent>
          </DropdownMenu>
          <Badge variant="outline" className="font-mono">
            {cluster}
          </Badge>
          <span className="text-sm text-muted-foreground">{namespace}</span>
          {podItem && (
            <span
              className={
                podStatus === 'Running'
                  ? 'inline-flex items-center rounded-full border border-green-200 bg-green-50 px-2 py-0.5 text-xs font-medium text-green-700 dark:border-green-800 dark:bg-green-950 dark:text-green-400'
                  : 'inline-flex items-center rounded-full border border-amber-200 bg-amber-50 px-2 py-0.5 text-xs font-medium text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-400'
              }
            >
              {podStatus || '—'}
            </span>
          )}
          <Button
            variant="destructive"
            size="sm"
            className="ml-auto"
            disabled={deleting}
            title={t('applications.detail.forceDeleteHint')}
            onClick={() => void handleForceDelete()}
          >
            <IconTrash className="w-4 h-4" />
            {armed
              ? t('applications.detail.podForceDeleteConfirm')
              : t('applications.detail.forceDeletePod')}
          </Button>
        </div>
      </div>

      {podMissing ? (
        <div className="flex flex-col items-center gap-2 rounded-xl border bg-card px-6 py-16 text-center">
          <div className="text-sm text-muted-foreground">
            {t('applications.detail.podNotFound')}
          </div>
          <Button size="sm" variant="outline" className="mt-2" asChild>
            <Link to={backTo}>{t('common.actions.back')}</Link>
          </Button>
        </div>
      ) : (
        <>
          <Tabs value={view} onValueChange={setView}>
            <TabsList>
              <TabsTrigger value="monitoring">
                {t('applications.detail.podMonitoring')}
              </TabsTrigger>
              <TabsTrigger value="terminal">
                {t('applications.detail.terminal')}
              </TabsTrigger>
              <TabsTrigger value="logs">
                {t('applications.detail.logs')}
              </TabsTrigger>
              <TabsTrigger value="files">
                {t('applications.detail.podFiles')}
              </TabsTrigger>
              <TabsTrigger value="yaml">YAML</TabsTrigger>
            </TabsList>
          </Tabs>

          {view === 'monitoring' && (
            <PodMonitoring
              namespace={namespace}
              podName={podName}
              cluster={cluster}
            />
          )}
          {view === 'terminal' && (
            <div className="h-[70vh]">
              <Terminal
                namespace={namespace}
                podName={podName}
                cluster={cluster}
                containers={podContainers.map((containerName) => ({
                  name: containerName,
                }))}
              />
            </div>
          )}
          {view === 'logs' && (
            <div className="h-[70vh]">
              <LogViewer
                namespace={namespace}
                podName={podName}
                cluster={cluster}
                containers={podContainers.map((containerName) => ({
                  name: containerName,
                }))}
              />
            </div>
          )}
          {view === 'files' && (
            <PodFileBrowser
              namespace={namespace}
              podName={podName}
              containers={podContainers.map((containerName) => ({
                name: containerName,
              }))}
              cluster={cluster}
            />
          )}
          {view === 'yaml' && (
            <pre className="max-h-[calc(100dvh-260px)] overflow-auto rounded-lg bg-zinc-950 p-4 font-mono text-xs leading-5 text-zinc-300">
              {yamlLoading
                ? t('common.messages.loading')
                : yamlError
                  ? translateError(yamlError, t)
                  : podObject
                    ? yaml.dump(podObject)
                    : '—'}
            </pre>
          )}
        </>
      )}
    </div>
  )
}
