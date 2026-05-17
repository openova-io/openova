/**
 * TenantsPage — /console/bss/tenants.
 *
 * Iframes the canonical back-office Tenants admin surface (SME tenant
 * roster, suspend / resume / impersonate, billing-account linkage).
 * See BssLayout.tsx for the architecture rationale (option B — iframe).
 */
import { BssIframe } from './BssLayout'

export function TenantsPage() {
  return <BssIframe path="tenants" title="BSS — Tenants" />
}
