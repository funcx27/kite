import { useState } from 'react'
import type {
  NotificationItem,
  NotificationType,
} from '@/portal/notifications/mock'
import {
  markAllRead,
  markRead,
  useNotifications,
} from '@/portal/notifications/store'
import { Check } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { cn, getAge } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

const TYPE_DOT_STYLES: Record<NotificationType, string> = {
  pipeline: 'bg-sky-500',
  approval: 'bg-amber-500',
  alert: 'bg-red-500',
  cert: 'bg-violet-500',
}

// Dot + text chip matching the network page's cluster status style; shared
// with the header bell.
export function NotificationTypeChip({ type }: { type: NotificationType }) {
  const { t } = useTranslation()
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full border bg-muted/40 px-2 py-0.5 text-xs">
      <span
        className={cn(
          'inline-block h-2 w-2 rounded-full',
          TYPE_DOT_STYLES[type]
        )}
      />
      {t(`notifications.type.${type}`)}
    </span>
  )
}

function NotificationRow({ item }: { item: NotificationItem }) {
  const { t } = useTranslation()
  const appHref =
    item.targetApp && item.targetSpace
      ? `/applications/${item.targetSpace}/${item.targetApp}`
      : null
  return (
    <div className="flex items-start justify-between gap-3 px-4 py-3">
      <div className="flex min-w-0 items-start gap-3">
        <span
          className={cn(
            'mt-1.5 h-2 w-2 shrink-0 rounded-full',
            item.read ? 'bg-transparent' : 'bg-primary'
          )}
        />
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <NotificationTypeChip type={item.type} />
            <span className={cn('text-sm', !item.read && 'font-medium')}>
              {t(`notifications.item.${item.title.key}`, item.title.values)}
            </span>
          </div>
          <p className="text-sm text-muted-foreground">
            {t(`notifications.item.${item.body.key}`, item.body.values)}
          </p>
          {appHref && (
            <Link to={appHref} className="text-xs text-primary hover:underline">
              {item.targetApp}
            </Link>
          )}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        <span className="text-xs text-muted-foreground">
          {t('common.messages.timeAgo', { time: getAge(item.createdAt) })}
        </span>
        {!item.read && (
          <Button
            variant="outline"
            size="sm"
            className="h-7"
            onClick={() => markRead(item.id)}
          >
            {t('notifications.actions.markRead')}
          </Button>
        )}
      </div>
    </div>
  )
}

export function NotificationsList() {
  const { t } = useTranslation()
  const notifications = useNotifications()
  const [filter, setFilter] = useState<'all' | 'unread'>('all')

  const unreadCount = notifications.filter((item) => !item.read).length
  const visible =
    filter === 'unread'
      ? notifications.filter((item) => !item.read)
      : notifications

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Tabs
          value={filter}
          onValueChange={(value) => setFilter(value as 'all' | 'unread')}
        >
          <TabsList>
            <TabsTrigger value="all">
              {t('notifications.filter.all')}
            </TabsTrigger>
            <TabsTrigger value="unread">
              {t('notifications.filter.unread')}
            </TabsTrigger>
          </TabsList>
        </Tabs>
        <div className="flex items-center gap-2">
          <span className="text-sm text-muted-foreground">
            {t('notifications.unreadCount', { count: unreadCount })}
          </span>
          <Button
            variant="outline"
            size="sm"
            disabled={unreadCount === 0}
            onClick={markAllRead}
          >
            <Check className="h-3.5 w-3.5" />
            {t('notifications.actions.markAllRead')}
          </Button>
        </div>
      </div>

      {visible.length === 0 ? (
        <div className="flex flex-col items-center gap-1 py-12 text-center">
          <p className="text-sm font-medium text-muted-foreground">
            {t('notifications.emptyTitle')}
          </p>
          <p className="text-sm text-muted-foreground">
            {t('notifications.emptyDescription')}
          </p>
        </div>
      ) : (
        // Seed data is newest-first; the store keeps that order on update.
        <Card>
          <CardContent className="divide-y p-0">
            {visible.map((item) => (
              <NotificationRow key={item.id} item={item} />
            ))}
          </CardContent>
        </Card>
      )}
    </div>
  )
}
