// Types mirror the planned network module API contract: a NetworkHost binds
// to a single cluster (the same name on another cluster is an independent
// registration) and owns host-level settings plus its route table; a
// NetworkRoute is one host+path → app+port rule.
export type NetworkConfigStatus =
  'synced' | 'drift' | 'pending' | 'error' | 'undeployed'

// Server-side route analysis enum: why the route does not match the live
// cluster. Empty/omitted array = no problem.
export type NetworkRouteIssue =
  'service-missing' | 'port-missing' | 'path-drift'

export interface NetworkRoute {
  path: string
  appName: string
  appNamespace: string
  port: number
  issues?: NetworkRouteIssue[]
  // Service-level annotations attached to this route's target service.
  annotations?: Record<string, string>
}

export interface NetworkCertificate {
  name: string
  expiresAt: string
  // CN + DNS SANs (deduped, at most 5); omitted on older responses.
  domains?: string[]
}

// One entry of the platform-wide annotation key catalog; the only
// applicability dimension is the IngressClass (controller family). The DB id
// addresses the row for admin edits (the key itself may contain "/" and is
// immutable anyway).
export interface NetworkAnnotationKey {
  id: number
  key: string
  description: string
  ingressClass: string
  defaultValue: string
}

// Cluster IngressClass with its controller image; the controller determines
// the controller family (brand icon, catalog filtering, ALB cert behavior).
export interface NetworkIngressClass {
  name: string
  controller: string
}

// Cluster Service with its declared ports; the route dialog offers these as
// realtime targets.
export interface NetworkService {
  name: string
  ports: Array<{ name: string; port: number }>
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
  // Failure reason while status is "error"; empty otherwise.
  lastError: string
}
