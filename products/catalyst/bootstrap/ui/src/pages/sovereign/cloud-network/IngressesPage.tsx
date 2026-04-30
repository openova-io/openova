/**
 * IngressesPage — placeholder list page for /cloud/network/ingresses
 * (P3 of #309). Pending the ingress informer (#321).
 */

import { CloudListPlaceholder } from '../cloud-list/CloudListPlaceholder'

export function IngressesPage() {
  return (
    <CloudListPlaceholder
      testId="cloud-ingresses"
      title="Ingresses"
      tagline="HTTP/HTTPS ingresses fronting workloads across clusters."
      bodyText="Ingress data is not in the current informer set. The ingress informer rollout is tracked separately."
      docsHref="https://github.com/openova-io/openova/issues/321"
    />
  )
}
