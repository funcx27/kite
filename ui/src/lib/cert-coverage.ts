// Wildcard-aware, case-insensitive match of a host against a certificate's
// CommonName + DNSNames: *.example.com covers every host ending in
// .example.com. Structural parameter: any certificate metadata shape with
// these fields fits (application wizard, network hosts).
export function hostCovered(
  host: string,
  cert: { commonName?: string; dnsNames?: string[] }
): boolean {
  const domains = [cert.commonName, ...(cert.dnsNames ?? [])]
    .filter(Boolean)
    .map((domain) => (domain as string).toLowerCase())
  return domains.some(
    (domain) =>
      host === domain ||
      (domain.startsWith('*.') && host.endsWith(domain.slice(1)))
  )
}
