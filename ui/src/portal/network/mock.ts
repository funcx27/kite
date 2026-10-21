// Demo dataset for the frontend-only network page; the real page replaces it
// with the network module API. Hosts bind to one cluster each; the data
// spreads the states across both clusters.
import { NetworkAnnotationKey, NetworkHostOverview } from './types'

// Options surfaced by the mock dialogs; the real page lists IngressClasses
// from the target cluster (validating presence before publish) and
// certificates from the cert module.
export const mockIngressClasses = ['nginx', 'traefik', 'kong']

export const mockCertCatalog: Record<string, string> = {
  'wildcard-example-com': '2027-03-15',
  'internal-wildcard': '2026-11-02',
}

// Platform-wide annotation key catalog (admin-maintained: builtin seed +
// batch import). The dialog filters it by the host's IngressClass and
// renders the description as help text.
export const mockAnnotationKeys: NetworkAnnotationKey[] = [
  {
    key: 'nginx.ingress.kubernetes.io/force-ssl-redirect',
    description: '强制将 HTTP 重定向到 HTTPS（true/false）',
    ingressClass: 'nginx',
  },
  {
    key: 'nginx.ingress.kubernetes.io/ssl-redirect',
    description: '配置了证书时是否重定向到 HTTPS，控制器默认 true',
    ingressClass: 'nginx',
  },
  {
    key: 'nginx.ingress.kubernetes.io/proxy-body-size',
    description: '请求体大小上限，控制器默认 1m，0 表示不限制',
    ingressClass: 'nginx',
  },
  {
    key: 'nginx.ingress.kubernetes.io/proxy-read-timeout',
    description: '后端读超时（秒），控制器默认 60',
    ingressClass: 'nginx',
  },
  {
    key: 'nginx.ingress.kubernetes.io/proxy-send-timeout',
    description: '向后端发送的超时（秒），控制器默认 60',
    ingressClass: 'nginx',
  },
  {
    key: 'nginx.ingress.kubernetes.io/proxy-connect-timeout',
    description: '连接后端的超时（秒），控制器默认 5',
    ingressClass: 'nginx',
  },
  {
    key: 'nginx.ingress.kubernetes.io/rewrite-target',
    description: '重写目标路径，如 /$1 配合捕获组使用',
    ingressClass: 'nginx',
  },
  {
    key: 'nginx.ingress.kubernetes.io/use-regex',
    description: '路径启用正则匹配（true/false）',
    ingressClass: 'nginx',
  },
  {
    key: 'nginx.ingress.kubernetes.io/whitelist-source-range',
    description: '客户端来源 IP 白名单，CIDR 逗号分隔',
    ingressClass: 'nginx',
  },
  {
    key: 'nginx.ingress.kubernetes.io/proxy-buffering',
    description: '响应缓冲开关，SSE/流式响应需设为 off',
    ingressClass: 'nginx',
  },
  {
    key: 'traefik.ingress.kubernetes.io/router.entrypoints',
    description: 'Traefik 路由入口点（如 web, websecure）',
    ingressClass: 'traefik',
  },
  {
    key: 'konghq.com/strip-path',
    description: '转发前剥去匹配的路径前缀（true/false）',
    ingressClass: 'kong',
  },
]

export const mockNetworkHosts: NetworkHostOverview[] = [
  {
    host: 'api.example.com',
    space: 'team-alpha',
    cluster: 'prod-cluster',
    ingressClass: 'nginx',
    status: 'synced',
    lbAddress: '203.0.113.10',
    certSynced: false,
    tlsCertificate: { name: 'wildcard-example-com', expiresAt: '2027-03-15' },
    annotations: {
      'nginx.ingress.kubernetes.io/force-ssl-redirect': 'true',
      'nginx.ingress.kubernetes.io/proxy-body-size': '8m',
    },
    routes: [
      {
        path: '/',
        appName: 'web-console',
        appNamespace: 'team-alpha',
        port: 80,
      },
      {
        path: '/api',
        appName: 'api-gateway',
        appNamespace: 'team-alpha',
        port: 8080,
      },
      {
        path: '/admin',
        appName: 'admin-portal',
        appNamespace: 'team-alpha',
        port: 3000,
      },
    ],
  },
  {
    host: 'docs.example.com',
    space: 'team-alpha',
    cluster: 'staging-cluster',
    ingressClass: 'nginx',
    status: 'drift',
    lbAddress: '198.51.100.21',
    certSynced: true,
    routes: [
      { path: '/', appName: 'docs-site', appNamespace: 'team-alpha', port: 80 },
    ],
  },
  {
    host: 'grafana.internal.example.com',
    space: 'platform',
    cluster: 'prod-cluster',
    ingressClass: 'nginx',
    status: 'pending',
    certSynced: true,
    tlsCertificate: { name: 'internal-wildcard', expiresAt: '2026-11-02' },
    annotations: {
      'nginx.ingress.kubernetes.io/proxy-read-timeout': '3600',
    },
    routes: [
      {
        path: '/',
        appName: 'grafana',
        appNamespace: 'platform',
        port: 3000,
      },
    ],
  },
  {
    host: 'console.example.com',
    space: 'platform',
    cluster: 'staging-cluster',
    ingressClass: 'nginx',
    status: 'error',
    certSynced: true,
    tlsCertificate: { name: 'wildcard-example-com', expiresAt: '2027-03-15' },
    routes: [
      {
        path: '/',
        appName: 'web-console',
        appNamespace: 'platform',
        port: 80,
      },
    ],
  },
]
