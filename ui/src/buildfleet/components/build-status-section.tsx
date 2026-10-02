import { useMemo } from 'react'
import {
  noCacheRegistryError,
  useBuildStatus,
  useCacheStat,
} from '@/buildfleet/api'
import { IconDatabase, IconRefresh, IconServer } from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'

import { BuildRuntimeStatus } from '@/types/buildfleet'
import { formatBytes, formatDate } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

interface ProviderCapacity {
  provider: string
  runtimes: number
  busy: number
  warm: number
  cpu: number
  memoryBytes: number
}

function groupProviderCapacity(
  runtimes: BuildRuntimeStatus[]
): ProviderCapacity[] {
  const map = new Map<string, ProviderCapacity>()
  for (const runtime of runtimes) {
    const key = runtime.provider || '-'
    const entry =
      map.get(key) ??
      ({
        provider: key,
        runtimes: 0,
        busy: 0,
        warm: 0,
        cpu: 0,
        memoryBytes: 0,
      } satisfies ProviderCapacity)
    entry.runtimes += 1
    if (runtime.state === 'busy') entry.busy += 1
    if (runtime.state === 'warm') entry.warm += 1
    entry.cpu += runtime.cpu
    entry.memoryBytes += runtime.memoryBytes
    map.set(key, entry)
  }
  return [...map.values()]
}

function formatIdle(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds)}s`
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`
  return `${Math.round(seconds / 3600)}h`
}

export function BuildStatusSection() {
  const { t } = useTranslation()
  const { data: status, refetch: refetchStatus } = useBuildStatus()
  const {
    data: cacheStat,
    error: cacheError,
    refetch: refetchCache,
  } = useCacheStat()

  const providerStats = useMemo(
    () => groupProviderCapacity(status?.runtimes ?? []),
    [status]
  )

  const noCacheRegistry =
    cacheError?.message?.includes(noCacheRegistryError) ?? false

  const refresh = () => {
    refetchStatus()
    refetchCache()
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">
          {t('buildfleet.status.title')}
        </h2>
        <Button variant="outline" size="sm" onClick={refresh}>
          <IconRefresh className="size-4" />
          {t('common.actions.refresh')}
        </Button>
      </div>

      <div className="grid grid-cols-1 gap-4 @5xl/main:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <IconServer className="size-4" />
              {t('buildfleet.status.pool')}
            </CardTitle>
            <CardDescription>
              {t('buildfleet.status.poolDescription')}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            {providerStats.length === 0 ? (
              <p className="text-sm text-muted-foreground">
                {t('common.messages.noData')}
              </p>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('buildfleet.status.provider')}</TableHead>
                    <TableHead>{t('buildfleet.status.runtimes')}</TableHead>
                    <TableHead>{t('buildfleet.status.busy')}</TableHead>
                    <TableHead>{t('buildfleet.status.warm')}</TableHead>
                    <TableHead>{t('buildfleet.status.cpu')}</TableHead>
                    <TableHead>{t('buildfleet.status.memory')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {providerStats.map((provider) => (
                    <TableRow key={provider.provider}>
                      <TableCell className="font-medium">
                        {provider.provider === '-'
                          ? t('buildfleet.status.unnamedProvider')
                          : provider.provider}
                      </TableCell>
                      <TableCell>{provider.runtimes}</TableCell>
                      <TableCell>{provider.busy}</TableCell>
                      <TableCell>{provider.warm}</TableCell>
                      <TableCell>{provider.cpu.toFixed(1)}</TableCell>
                      <TableCell>{formatBytes(provider.memoryBytes)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}

            {(status?.runtimes.length ?? 0) > 0 && (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('buildfleet.status.runtimeId')}</TableHead>
                    <TableHead>{t('buildfleet.status.state')}</TableHead>
                    <TableHead>{t('buildfleet.status.addr')}</TableHead>
                    <TableHead>{t('buildfleet.status.idle')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {status!.runtimes.map((runtime) => (
                    <TableRow key={runtime.id}>
                      <TableCell className="font-mono text-xs">
                        {runtime.id}
                      </TableCell>
                      <TableCell>{runtime.state}</TableCell>
                      <TableCell className="font-mono text-xs">
                        {runtime.addr}
                      </TableCell>
                      <TableCell>{formatIdle(runtime.idleSeconds)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <IconDatabase className="size-4" />
              {t('buildfleet.status.cache')}
            </CardTitle>
            <CardDescription>
              {cacheStat
                ? t('buildfleet.status.cacheDescription', {
                    total: formatBytes(cacheStat.totalBytes),
                  })
                : t('buildfleet.status.cacheDescriptionEmpty')}
            </CardDescription>
          </CardHeader>
          <CardContent>
            {noCacheRegistry ? (
              <p className="text-sm text-muted-foreground">
                {t('buildfleet.status.noCacheRegistry')}
              </p>
            ) : (cacheStat?.projects.length ?? 0) === 0 ? (
              <p className="text-sm text-muted-foreground">
                {t('common.messages.noData')}
              </p>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('buildfleet.status.provider')}</TableHead>
                    <TableHead>{t('buildfleet.status.project')}</TableHead>
                    <TableHead>{t('buildfleet.status.tags')}</TableHead>
                    <TableHead>{t('buildfleet.status.layers')}</TableHead>
                    <TableHead>{t('buildfleet.status.exclusive')}</TableHead>
                    <TableHead>{t('buildfleet.status.shared')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {cacheStat!.projects.map((project) => (
                    <TableRow key={`${project.provider}/${project.project}`}>
                      <TableCell>{project.provider}</TableCell>
                      <TableCell className="font-mono text-xs">
                        {project.project}
                      </TableCell>
                      <TableCell>{project.tags}</TableCell>
                      <TableCell>{project.layers}</TableCell>
                      <TableCell>
                        {formatBytes(project.exclusiveBytes)}
                      </TableCell>
                      <TableCell>{formatBytes(project.sharedBytes)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t('buildfleet.status.tasks')}</CardTitle>
          <CardDescription>
            {t('buildfleet.status.tasksDescription')}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {(status?.tasks.length ?? 0) === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t('buildfleet.status.noTasks')}
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('buildfleet.status.runtimeId')}</TableHead>
                  <TableHead>{t('buildfleet.status.source')}</TableHead>
                  <TableHead>{t('buildfleet.status.outputRef')}</TableHead>
                  <TableHead>{t('buildfleet.status.startedAt')}</TableHead>
                  <TableHead>{t('buildfleet.status.attempt')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {status!.tasks.map((task) => (
                  <TableRow key={`${task.runtimeId}/${task.startedUnix}`}>
                    <TableCell className="font-mono text-xs">
                      {task.runtimeId}
                    </TableCell>
                    <TableCell className="max-w-48 truncate font-mono text-xs">
                      {task.source}
                    </TableCell>
                    <TableCell className="max-w-48 truncate font-mono text-xs">
                      {task.outputRef || '-'}
                    </TableCell>
                    <TableCell>
                      {formatDate(
                        new Date(task.startedUnix * 1000).toISOString()
                      )}
                    </TableCell>
                    <TableCell>{task.attempt}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
