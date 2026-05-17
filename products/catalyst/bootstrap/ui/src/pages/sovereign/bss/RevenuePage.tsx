/**
 * RevenuePage — /console/bss/revenue.
 *
 * Wave 6 PR 1 (Option B step 1): wraps in PortalShell via
 * BssSectionShell. Iframe content preserved; Wave 6 PR 4 native-ports.
 */
import { BssSectionShell } from './BssSectionShell'

export function RevenuePage() {
  return <BssSectionShell path="revenue" title="BSS — Revenue" />
}
