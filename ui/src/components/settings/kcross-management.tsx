import { useMemo, useState } from 'react'
import { IconLink, IconPlug, IconRefresh, IconServer } from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { createCluster } from '@/lib/api'
import {
  KcrossPeer,
  updateKcrossSetting,
  testKcrossSetting,
  useCreateKcrossBinding,
  useDeleteKcrossBinding,
  useKcrossPeers,
  useKcrossSetting,
} from '@/lib/api/kcross'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'

import { Action, ActionTable } from '../action-table'

export function KcrossManagement() {
  const { t } = useTranslation()
  const { data: setting, isLoading } = useKcrossSetting()
  const settingReady = Boolean(setting?.enabled)
  const { data: peers = [], isLoading: peersLoading } = useKcrossPeers(settingReady)
  const createBinding = useCreateKcrossBinding()
  const deleteBinding = useDeleteKcrossBinding()

  const [addingPeer, setAddingPeer] = useState<KcrossPeer | null>(null)

  const columns = useMemo(
    () => [
      {
        id: 'peer',
        header: t('kcrossManagement.peer', 'Peer'),
        cell: ({ row: { original: peer } }: { row: { original: KcrossPeer } }) => (
          <div className="flex items-center gap-2">
            <span
              className={`inline-block h-2 w-2 rounded-full ${
                peer.status === 'online' ? 'bg-green-500' : 'bg-red-500'
              }`}
              title={peer.status}
            />
            <span className="font-medium font-mono">{peer.id}</span>
          </div>
        ),
      },
      {
        id: 'connectedAt',
        header: t('kcrossManagement.connectedAt', 'Connected At'),
        cell: ({ row: { original: peer } }: { row: { original: KcrossPeer } }) =>
          peer.connectedAt ? new Date(peer.connectedAt).toLocaleString() : '-',
      },
      {
        id: 'capabilities',
        header: t('kcrossManagement.capabilities', 'Capabilities'),
        cell: ({ row: { original: peer } }: { row: { original: KcrossPeer } }) => (
          <div className="flex flex-wrap gap-1">
            {peer.capabilities.map((cap) => (
              <Badge
                key={cap.name}
                variant={cap.health === 'healthy' ? 'secondary' : 'destructive'}
                className="font-mono"
              >
                {cap.name} ({cap.kind})
              </Badge>
            ))}
            {peer.capabilities.length === 0 && '-'}
          </div>
        ),
      },
      {
        id: 'prometheus',
        header: t('kcrossManagement.prometheus', 'Prometheus'),
        cell: ({ row: { original: peer } }: { row: { original: KcrossPeer } }) =>
          peer.prometheus?.endpoint ? (
            <code className="text-xs bg-muted px-1.5 py-0.5 rounded">
              {peer.prometheus.endpoint}
            </code>
          ) : (
            '-'
          ),
      },
      {
        id: 'bound',
        header: t('kcrossManagement.bound', 'Onboarded'),
        cell: ({ row: { original: peer } }: { row: { original: KcrossPeer } }) =>
          peer.bound ? (
            <Badge variant="outline">{peer.clusterName || peer.clusterId}</Badge>
          ) : (
            <span className="text-muted-foreground">-</span>
          ),
      },
    ],
    [t]
  )

  const actions: Action<KcrossPeer>[] = [
    {
      label: t('common.actions.add', 'Add'),
      onClick: (peer) => setAddingPeer(peer),
      shouldDisable: (peer) => peer.bound || !peer.kubernetes,
    },
    {
      label: t('common.actions.unbind', 'Unbind'),
      onClick: (peer) => {
        if (!peer.clusterId) return
        deleteBinding.mutate(peer.clusterId, {
          onSuccess: () =>
            toast.success(
              t('kcrossManagement.messages.unbound', 'Cluster unbound from peer')
            ),
          onError: (err: Error) => toast.error(err.message),
        })
      },
      shouldDisable: (peer) => !peer.bound,
    },
  ]

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <IconPlug className="h-5 w-5" />
            {t('kcrossManagement.title', 'kcross Gateway')}
          </CardTitle>
        </CardHeader>
        <CardContent>
          {isLoading ? (
            <p className="text-sm text-muted-foreground">Loading…</p>
          ) : (
            <GatewaySettingForm initial={setting} />
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <IconServer className="h-5 w-5" />
            {t('kcrossManagement.discoveredPeers', 'Discovered Peers')}
          </CardTitle>
        </CardHeader>
        <CardContent>
          {!settingReady ? (
            <p className="text-sm text-muted-foreground">
              {t(
                'kcrossManagement.notConfigured',
                'Configure and enable the gateway first to discover peers.'
              )}
            </p>
          ) : peersLoading ? (
            <p className="text-sm text-muted-foreground">Loading…</p>
          ) : (
            <ActionTable
              data={peers}
              columns={columns as never[]}
              actions={actions}
            />
          )}
        </CardContent>
      </Card>

      <AddPeerClusterDialog
        peer={addingPeer}
        onOpenChange={(open) => !open && setAddingPeer(null)}
        onSubmit={(name) => {
          const peer = addingPeer
          if (!peer) return
          // 零凭证纳管：仅起名，kubeconfig 由绑定接口生成（server 指向网关反代）
          createCluster({ name })
            .then((created) =>
              createBinding.mutateAsync({
                clusterId: created.id,
                peerId: peer.id,
                gateway: peer.gateway,
                capability: peer.kubernetes?.name,
              })
            )
            .then(() => {
              toast.success(
                t('kcrossManagement.messages.added', 'Cluster added and bound to peer')
              )
              setAddingPeer(null)
            })
            .catch((err: Error) => toast.error(err.message))
        }}
      />
    </div>
  )
}

function GatewaySettingForm({
  initial,
}: {
  initial: { enabled: boolean; gateways: string[]; adminTokenConfigured: boolean } | undefined
}) {
  const { t } = useTranslation()

  const [enabled, setEnabled] = useState(Boolean(initial?.enabled))
  const [gateways, setGateways] = useState((initial?.gateways || []).join(','))
  const [adminToken, setAdminToken] = useState('')
  const [testing, setTesting] = useState(false)
  const [saving, setSaving] = useState(false)

  const payload = () => ({
    enabled,
    gateways: gateways.trim(),
    adminToken: adminToken.trim() || undefined,
  })

  const handleTest = async () => {
    setTesting(true)
    try {
      const result = await testKcrossSetting(payload())
      toast.success(
        t('kcrossManagement.messages.testOk', 'Connected, {{count}} peers found', {
          count: result.peerCount,
        })
      )
    } catch (err) {
      toast.error((err as Error).message)
    } finally {
      setTesting(false)
    }
  }

  const handleSave = async () => {
    setSaving(true)
    try {
      await updateKcrossSetting(payload())
      setAdminToken('')
      toast.success(t('common.messages.saved', 'Settings saved'))
    } catch (err) {
      toast.error((err as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <Label>{t('kcrossManagement.enabled', 'Enabled')}</Label>
          <p className="text-sm text-muted-foreground">
            {t(
              'kcrossManagement.enabledDescription',
              'Discover peers through the kcross gateway and connect clusters via tunnel'
            )}
          </p>
        </div>
        <Switch checked={enabled} onCheckedChange={setEnabled} />
      </div>
      <div className="grid gap-2">
        <Label htmlFor="kcross-gateways">
          {t('kcrossManagement.gateways', 'Gateway URLs')}
        </Label>
        <Input
          id="kcross-gateways"
          placeholder="http://kcross-gateway-1:18080,http://kcross-gateway-2:18080"
          value={gateways}
          onChange={(e) => setGateways(e.target.value)}
        />
        <p className="text-xs text-muted-foreground">
          {t(
            'kcrossManagement.gatewaysHint',
            'Multiple gateways separated by commas'
          )}
        </p>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="kcross-admin-token">
          {t('kcrossManagement.adminToken', 'Admin Token')}
        </Label>
        <Input
          id="kcross-admin-token"
          type="password"
          placeholder={
            initial?.adminTokenConfigured
              ? t('kcrossManagement.tokenKeepPlaceholder', 'Leave empty to keep current token')
              : 'kcross gateway authToken'
          }
          value={adminToken}
          onChange={(e) => setAdminToken(e.target.value)}
        />
      </div>
      <div className="flex gap-2">
        <Button variant="outline" onClick={handleTest} disabled={testing || !gateways.trim()}>
          <IconRefresh className="mr-2 h-4 w-4" />
          {testing
            ? t('common.actions.testing', 'Testing…')
            : t('kcrossManagement.testConnection', 'Test Connection')}
        </Button>
        <Button onClick={handleSave} disabled={saving}>
          <IconLink className="mr-2 h-4 w-4" />
          {saving ? t('common.actions.saving', 'Saving…') : t('common.actions.save', 'Save')}
        </Button>
      </div>
    </div>
  )
}

function AddPeerClusterDialog({
  peer,
  onOpenChange,
  onSubmit,
}: {
  peer: KcrossPeer | null
  onOpenChange: (open: boolean) => void
  onSubmit: (name: string) => void
}) {
  const { t } = useTranslation()

  const [name, setName] = useState('')
  const submitEnabled = peer !== null && name.trim() !== ''

  return (
    <Dialog open={peer !== null} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {t('kcrossManagement.addCluster', 'Onboard Cluster from Peer {{peer}}', {
              peer: peer?.id ?? '',
            })}
          </DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div className="grid gap-2">
            <Label htmlFor="kcross-cluster-name">
              {t('common.fields.name', 'Name')}
            </Label>
            <Input
              id="kcross-cluster-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <p className="text-sm text-muted-foreground">
            {t(
              'kcrossManagement.zeroCredHint',
              'No credentials needed: the cluster connects through the gateway proxy and the peer injects its service account. Effective permissions = kite RBAC ∩ peer SA RBAC.'
            )}
          </p>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.actions.cancel', 'Cancel')}
          </Button>
          <Button
            disabled={!submitEnabled}
            onClick={() => onSubmit(name.trim())}
          >
            {t('common.actions.add', 'Add')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
