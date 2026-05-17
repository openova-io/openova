/**
 * OrdersPage — /console/bss/orders.
 *
 * Iframes the canonical back-office Orders surface. See BssLayout.tsx
 * for the architecture rationale (option B — iframe).
 */
import { BssIframe } from './BssLayout'

export function OrdersPage() {
  return <BssIframe path="orders" title="BSS — Orders" />
}
