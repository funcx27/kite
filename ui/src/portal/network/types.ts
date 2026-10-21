// Types mirror the planned network module API contract: a NetworkHost binds
// to a single cluster (the same name on another cluster is an independent
// registration) and owns host-level settings plus its route table; a
// NetworkRoute is one host+path → app+port rule.
export type NetworkConfigStatus =
  'synced' | 'drift' | 'pending' | 'error' | 'undeployed'

export interface NetworkRoute {
  path: string
  appName: string
  appNamespace: string
  port: number
}

export interface NetworkCertificate {
  name: string
  expiresAt: string
}

// One entry of the platform-wide annotation key catalog; the only
// applicability dimension is the IngressClass (controller family).
export interface NetworkAnnotationKey {
  key: string
  description: string
  ingressClass: string
}

export interface NetworkHostOverview {
  host: string
  space: string
  cluster: string
  ingressClass: string
  status: NetworkConfigStatus
  certSynced: boolean
  lbAddress?: string
  tlsCertificate?: NetworkCertificate
  // Host-level Ingress metadata annotations; applied to every path.
  annotations?: Record<string, string>
  routes: NetworkRoute[]
}
