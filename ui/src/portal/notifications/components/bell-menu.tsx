// Notification bell for the portal header: reads the shared mock store so
// the badge, this popover and the inbox page stay in sync.
import { useState } from 'react'
import { NotificationTypeChip } from '@/portal/notifications/components/notifications-list'
import type { NotificationItem } from '@/portal/notifications/mock'
import {
  markAllRead,
  markRead,
  useNotifications,
} from '@/portal/notifications/store'
import { Bell } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { cn, getAge } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'

const RECENT_COUNT = 5

function BellRow({
  item,
  onMarkRead,
}: {
  item: NotificationItem
  onMarkRead: (id: string) => void
}) {
  const { t } = useTranslation()
  return (
    <button
      type="button"
      onClick={() => onMarkRead(item.id)}
      className="flex w-full items-start gap-2 px-3 py-2 text-left hover:bg-accent/60"
    >
      <span
        className={cn(
          'mt-1.5 h-2 w-2 shrink-0 rounded-full',
          item.read ? 'bg-transparent' : 'bg-primary'
        )}
      />
      <span className="min-w-0 flex-1 space-y-1">
        <span className="block">
          <NotificationTypeChip type={item.type} />
        </span>
        <span className="block truncate text-sm">
          {t(`notifications.item.${item.title.key}`, item.title.values)}
        </span>
        <span className="block text-xs text-muted-foreground">
          {t('common.messages.timeAgo', { time: getAge(item.createdAt) })}
        </span>
      </span>
    </button>
  )
}

export function NotificationBell() {
  const { t } = useTranslation()
  const notifications = useNotifications()
  const [open, setOpen] = useState(false)

  const unreadCount = notifications.filter((item) => !item.read).length
  const recent = notifications.slice(0, RECENT_COUNT)

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="relative"
          aria-label={t('notifications.bell.title')}
        >
          <Bell className="h-[1.2rem] w-[1.2rem]" />
          {unreadCount > 0 && (
            <span className="absolute -right-0.5 -top-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-red-500 px-1 text-[10px] font-medium leading-none text-white">
              {unreadCount}
            </span>
          )}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 p-0">
        <div className="flex items-center justify-between border-b px-3 py-2">
          <span className="text-sm font-medium">
            {t('notifications.bell.title')}
          </span>
          <span className="text-xs text-muted-foreground">
            {t('notifications.unreadCount', { count: unreadCount })}
          </span>
        </div>
        {recent.length === 0 ? (
          <p className="px-3 py-8 text-center text-sm text-muted-foreground">
            {t('notifications.bell.empty')}
          </p>
        ) : (
          <div className="max-h-80 overflow-y-auto">
            {recent.map((item) => (
              <BellRow key={item.id} item={item} onMarkRead={markRead} />
            ))}
          </div>
        )}
        <div className="flex items-center justify-between border-t px-2 py-1.5">
          <Button
            variant="ghost"
            size="sm"
            disabled={unreadCount === 0}
            onClick={markAllRead}
          >
            {t('notifications.actions.markAllRead')}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            asChild
            onClick={() => setOpen(false)}
          >
            <Link to="/notifications">{t('notifications.bell.viewAll')}</Link>
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  )
}
