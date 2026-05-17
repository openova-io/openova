/**
 * BillingPage — /console/bss/billing.
 *
 * Iframes the canonical back-office Billing surface from the admin Pod
 * (sme namespace, served via marketplace.<sov-fqdn>/back-office/billing/).
 * The Sovereign Console URL stays clean (`/bss/billing`) so the founder
 * #1 requirement — "another menu under console like console.<sov>/bss" —
 * is met while reusing the existing production back-office UI.
 *
 * See BssLayout.tsx for the architecture rationale (option B — iframe).
 */
import { BssIframe } from './BssLayout'

export function BillingPage() {
  return <BssIframe path="billing" title="BSS — Billing" />
}
