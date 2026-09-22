import { useMemo, useState } from 'react'
import {
  IconLink,
  IconPlug,
  IconRefresh,
  IconSearch,
  IconServer,
  IconTerminal,
} from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { createCluster } from '@/lib/api'
import {
  KcrossPeer,
  updateKcrossSetting,
  testKcrossSetting,
  useCreateKcrossBinding,
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
import { KcrossPeerTerminal } from './kcross-peer-terminal'

export function KcrossManagement() {
  const { t } = useTranslation()
  const { data: setting, isLoading } = useKcrossSetting()
  const settingReady = Boolean(setting?.enabled)
  const { data: peers = [], isLoading: peersLoading } = useKcrossPeers(settingReady)
  const createBinding = useCreateKcrossBinding()

  const [addingPeer, setAddingPeer] = useState<KcrossPeer | null>(null)
  const [detailPeer, setDetailPeer] = useState<KcrossPeer | null>(null)
  const [terminalPeer, setTerminalPeer] = useState<KcrossPeer | null>(null)
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState<'all' | 'unbound' | 'bound' | 'online' | 'offline'>(
    'all'
  )

  const filteredPeers = useMemo(() => {
    const keyword = search.trim().toLowerCase()
    return peers.filter((peer) => {
      if (
        keyword &&
        !peer.id.toLowerCase().includes(keyword) &&
        !peer.capabilities.some((cap) => cap.name.toLowerCase().includes(keyword)) &&
        !(peer.clusterName || '').toLowerCase().includes(keyword)
      ) {
        return false
      }
      switch (filter) {
        case 'unbound':
          return !peer.bound
        case 'bound':
          return peer.bound
        case 'online':
          return peer.status === 'online'
        case 'offline':
          return peer.status !== 'online'
      }
      return true
    })
  }, [peers, search, filter])

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
            <button
              type="button"
              className={`font-mono hover:underline ${
                peer.bound ? 'font-medium' : 'text-muted-foreground'
              }`}
              title={
                peer.bound
                  ? t('kcrossManagement.boundPeerHint', 'Onboarded from this peer')
                  : t('kcrossManagement.unboundPeerHint', 'Discovered, not onboarded yet')
              }
              onClick={() => setDetailPeer(peer)}
            >
              {peer.id}
            </button>
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
            <Badge variant="secondary">{peer.clusterName || peer.clusterId}</Badge>
          ) : (
            <Badge
              variant="outline"
              className="border-dashed text-muted-foreground"
            >
              {t('kcrossManagement.unbound', 'Unbound')}
            </Badge>
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
            <div className="space-y-3">
              <div className="flex flex-wrap items-center gap-2">
                <div className="relative flex-1 min-w-48">
                  <IconSearch className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
                  <Input
                    value={search}
                    onChange={(e) => setSearch(e.target.value)}
                    placeholder={t('kcrossManagement.searchPlaceholder', 'Search peer / capability / cluster')}
                    className="pl-8"
                  />
                </div>
                <div className="flex items-center gap-1">
                  {(
                    [
                      ['all', 'filterAll'],
                      ['unbound', 'filterUnbound'],
                      ['bound', 'filterBound'],
                      ['online', 'filterOnline'],
                      ['offline', 'filterOffline'],
                    ] as const
                  ).map(([value, key]) => (
                    <button
                      key={value}
                      type="button"
                      onClick={() => setFilter(value)}
                      className={`rounded-md px-2.5 py-1.5 text-sm transition-colors ${
                        filter === value
                          ? 'bg-primary text-primary-foreground'
                          : 'bg-muted text-muted-foreground hover:bg-muted/70'
                      }`}
                    >
                      {t(`kcrossManagement.${key}`, value)}
                    </button>
                  ))}
                </div>
              </div>
              <ActionTable
                data={filteredPeers}
                columns={columns as never[]}
                actions={actions}
              />
            </div>
          )}
        </CardContent>
      </Card>

      <PeerDetailDialog
        peer={detailPeer}
        onOpenChange={(open) => !open && setDetailPeer(null)}
        onTerminal={() => {
          if (detailPeer) {
            setTerminalPeer(detailPeer)
            setDetailPeer(null)
          }
        }}
      />

      {terminalPeer && (
        <KcrossPeerTerminal
          peerId={terminalPeer.id}
          gateway={terminalPeer.gateway}
          onClose={() => setTerminalPeer(null)}
        />
      )}

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

function PeerDetailDialog({
  peer,
  onOpenChange,
  onTerminal,
}: {
  peer: KcrossPeer | null
  onOpenChange: (open: boolean) => void
  onTerminal?: () => void
}) {
  const { t } = useTranslation()

  return (
    <Dialog open={peer !== null} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {t('kcrossManagement.peerDetail', 'Peer {{peer}}', {
              peer: peer?.id ?? '',
            })}
          </DialogTitle>
        </DialogHeader>
        {peer && (
          <div className="space-y-4">
            <div className="grid grid-cols-[auto_1fr] items-center gap-x-6 gap-y-2 text-sm">
              <span className="text-muted-foreground">
                {t('kcrossManagement.peerStatus', 'Status')}
              </span>
              <span className="flex items-center gap-1.5">
                <span
                  className={`inline-block h-2 w-2 rounded-full ${
                    peer.status === 'online' ? 'bg-green-500' : 'bg-red-500'
                  }`}
                />
                {peer.status}
              </span>
              <span className="text-muted-foreground">
                {t('kcrossManagement.connectedAt', 'Connected At')}
              </span>
              <span>
                {peer.connectedAt ? new Date(peer.connectedAt).toLocaleString() : '-'}
              </span>
              <span className="text-muted-foreground">
                {t('kcrossManagement.gateway', 'Gateway')}
              </span>
              <span className="break-all font-mono text-xs">{peer.gateway}</span>
              <span className="text-muted-foreground">
                {t('kcrossManagement.bound', 'Onboarded')}
              </span>
              <span>
                {peer.bound ? (
                  <Badge variant="secondary">{peer.clusterName || peer.clusterId}</Badge>
                ) : (
                  <Badge
                    variant="outline"
                    className="border-dashed text-muted-foreground"
                  >
                    {t('kcrossManagement.unbound', 'Unbound')}
                  </Badge>
                )}
              </span>
            </div>
            {peer.status === 'online' && (
              <Button
                variant="outline"
                onClick={() => {
                  onOpenChange(false)
                  onTerminal?.()
                }}
              >
                <IconTerminal className="mr-2 h-4 w-4" />
                {t('kcrossManagement.openTerminal', 'Open Terminal')}
              </Button>
            )}
            <div className="space-y-2">
              <Label>{t('kcrossManagement.capabilities', 'Capabilities')}</Label>
              {peer.capabilities.map((cap) => (
                <div key={cap.name} className="space-y-1.5 rounded-md border p-2.5">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm font-medium">{cap.name}</span>
                    <Badge variant="outline">{cap.kind}</Badge>
                    <Badge variant={cap.health === 'healthy' ? 'secondary' : 'destructive'}>
                      {cap.health}
                    </Badge>
                  </div>
                  <div className="break-all font-mono text-xs leading-5">
                    {cap.access ? (
                      <>
                        <span>{cap.access}</span>
                        <span className="text-muted-foreground">
                          {'  ->  '}
                          {cap.endpoint || '-'}
                        </span>
                      </>
                    ) : (
                      <span className="text-muted-foreground">{cap.endpoint || '-'}</span>
                    )}
                  </div>
                </div>
              ))}
              {peer.capabilities.length === 0 && (
                <p className="text-sm text-muted-foreground">-</p>
              )}
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
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
