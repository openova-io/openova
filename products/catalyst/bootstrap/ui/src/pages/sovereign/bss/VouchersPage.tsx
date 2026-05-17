/**
 * VouchersPage — /console/bss/vouchers.
 *
 * Iframes the canonical back-office Vouchers surface. See BssLayout.tsx
 * for the architecture rationale (option B — iframe).
 *
 * Backend already shipped (issue #828): /billing/vouchers/{issue,list,
 * revoke,redeem-preview}. The UI surface for issuing + revoking lives
 * in the back-office Pod.
 */
import { BssIframe } from './BssLayout'

export function VouchersPage() {
  return <BssIframe path="vouchers" title="BSS — Vouchers" />
}
