import { useState } from 'react'
import { NotificationsList } from '@/portal/notifications/components/notifications-list'
import { SubscriptionRules } from '@/portal/notifications/components/subscription-rules'
import { useTranslation } from 'react-i18next'

import { usePageTitle } from '@/hooks/use-page-title'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

export function NotificationsPage() {
  const { t } = useTranslation()
  usePageTitle(t('notifications.title'))
  const [tab, setTab] = useState('notifications')

  return (
    <div className="flex flex-col gap-4">
      <div className="space-y-1.5">
        <h1 className="text-2xl font-bold">{t('notifications.title')}</h1>
        <p className="text-sm text-muted-foreground">
          {t('notifications.description')}
        </p>
      </div>
      <Tabs value={tab} onValueChange={setTab} className="gap-4">
        <TabsList>
          <TabsTrigger value="notifications">
            {t('notifications.tabs.notifications')}
          </TabsTrigger>
          <TabsTrigger value="rules">
            {t('notifications.tabs.rules')}
          </TabsTrigger>
        </TabsList>
        <TabsContent value="notifications">
          <NotificationsList />
        </TabsContent>
        <TabsContent value="rules">
          <SubscriptionRules />
        </TabsContent>
      </Tabs>
    </div>
  )
}
