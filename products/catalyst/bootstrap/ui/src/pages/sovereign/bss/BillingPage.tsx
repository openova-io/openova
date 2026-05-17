/**
 * BillingPage — /console/bss/billing.
 *
 * Wave 6 PR 1 (Option B step 1): wraps in PortalShell via
 * BssSectionShell so chrome matches the rest of the Sovereign Console.
 * Iframe content is preserved for now — Wave 6 PR 2 native-ports it.
 */
import { BssSectionShell } from './BssSectionShell'

export function BillingPage() {
  return <BssSectionShell path="billing" title="BSS — Billing" />
}
