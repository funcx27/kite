import type { NetworkIngressClass } from '@/portal/network/types'

// Controller-image → controller family. Mirrors the backend's
// ControllerFamily so brand icons, catalog filtering and the ALB cert
// behavior agree; kept inside portal/network because src/lib must not
// import portal types.
export type IngressFamily = 'nginx' | 'traefik' | 'kong' | 'alb' | 'other'

export function familyOfController(controller: string): IngressFamily {
  if (
    controller.startsWith('k8s.io/ingress-nginx') ||
    controller.startsWith('kubernetes.io/ingress-nginx')
  ) {
    return 'nginx'
  }
  if (controller.includes('traefik')) return 'traefik'
  if (controller.startsWith('konghq.com/')) return 'kong'
  if (controller.startsWith('ingress.k8s.alibabacloud/')) return 'alb'
  return 'other'
}

// Family of a class by name; unknown classes (list still loading, echoed
// stale value) resolve to "other".
export function familyOf(
  className: string,
  classes: NetworkIngressClass[]
): IngressFamily {
  const controller =
    classes.find((entry) => entry.name === className)?.controller ?? ''
  return familyOfController(controller)
}

// Catalog entries match a host either by its exact class name or by the
// controller-family label (the catalog's class field is free-form).
export function catalogEntryMatches(
  entryClass: string,
  className: string,
  classes: NetworkIngressClass[]
): boolean {
  return entryClass === className || entryClass === familyOf(className, classes)
}
