/**
 * DnsZonesPage — placeholder list page for /cloud/network/dns-zones
 * (P3 of #309). Pending the external-dns informer (#321).
 */

import { CloudListPlaceholder } from '../cloud-list/CloudListPlaceholder'

export function DnsZonesPage() {
  return (
    <CloudListPlaceholder
      testId="cloud-dns-zones"
      title="DNS Zones"
      tagline="DNS zones managed by the Sovereign control plane (Dynadot, PowerDNS)."
      bodyText="DNS-zone data is not in the current informer set. The external-dns informer rollout is tracked separately."
      docsHref="https://github.com/openova-io/openova/issues/321"
    />
  )
}
