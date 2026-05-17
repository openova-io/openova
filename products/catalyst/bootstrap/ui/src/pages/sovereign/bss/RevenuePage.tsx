/**
 * RevenuePage — /console/bss/revenue.
 *
 * Iframes the canonical back-office Revenue surface. See BssLayout.tsx
 * for the architecture rationale (option B — iframe).
 */
import { BssIframe } from './BssLayout'

export function RevenuePage() {
  return <BssIframe path="revenue" title="BSS — Revenue" />
}
