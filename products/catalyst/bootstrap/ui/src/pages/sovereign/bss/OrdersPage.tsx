/**
 * OrdersPage — /console/bss/orders.
 *
 * Wave 6 PR 1 (Option B step 1): wraps in PortalShell via
 * BssSectionShell. Iframe content preserved; Wave 6 PR 3 native-ports.
 */
import { BssSectionShell } from './BssSectionShell'

export function OrdersPage() {
  return <BssSectionShell path="orders" title="BSS — Orders" />
}
