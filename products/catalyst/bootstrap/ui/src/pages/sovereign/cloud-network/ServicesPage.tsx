/**
 * ServicesPage — placeholder list page for /cloud/network/services
 * (P3 of #309). The data wiring depends on the service-informer
 * rollout tracked in #321; the page surface ships now so the route
 * exists.
 */

import { CloudListPlaceholder } from '../cloud-list/CloudListPlaceholder'

export function ServicesPage() {
  return (
    <CloudListPlaceholder
      testId="cloud-services"
      title="Services"
      tagline="Per-namespace Kubernetes services across all clusters."
      bodyText="Services data is not in the current informer set. The service informer rollout is tracked separately."
      docsHref="https://github.com/openova-io/openova/issues/321"
    />
  )
}
