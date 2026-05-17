/**
 * TenantsPage — /console/bss/tenants.
 *
 * Wave 6 PR 1 (Option B step 1): wraps in PortalShell via
 * BssSectionShell. Iframe content preserved; Wave 6 PR 6 native-ports.
 */
import { BssSectionShell } from './BssSectionShell'

export function TenantsPage() {
  return <BssSectionShell path="tenants" title="BSS — Tenants" />
}
