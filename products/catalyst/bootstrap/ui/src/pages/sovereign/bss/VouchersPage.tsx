/**
 * VouchersPage — /console/bss/vouchers.
 *
 * Wave 6 PR 1 (Option B step 1): wraps in PortalShell via
 * BssSectionShell. Iframe content preserved; Wave 6 PR 5 native-ports.
 *
 * Backend already shipped (issue #828): /billing/vouchers/{issue,list,
 * revoke,redeem-preview}. The UI surface for issuing + revoking lives
 * in the back-office Pod for this PR.
 */
import { BssSectionShell } from './BssSectionShell'

export function VouchersPage() {
  return <BssSectionShell path="vouchers" title="BSS — Vouchers" />
}
