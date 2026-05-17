/**
 * BssSectionShell — PortalShell + iframe wrapper for /bss/<section>.
 *
 * Wave 6 PR 1 (Option B step 1): replaces the BssLayout outlet pattern.
 * Each per-section page (Billing/Orders/Revenue/Vouchers/Tenants)
 * wraps in this shell so the BSS sub-pages share the SAME PortalShell
 * chrome as the rest of the Sovereign Console — no more bespoke
 * BssLayout tab strip; the sidebar's BSS group covers navigation.
 *
 * PRs 2-6 of Wave 6 will native-port each section's iframe content
 * into React. For now the iframe is preserved to keep the back-office
 * surfaces reachable; only the chrome around them changes in this PR.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 the back-office host derives
 * from DETECTED_MODE.sovereignFQDN at runtime, never baked.
 */

import { Link } from '@tanstack/react-router'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { PortalShell } from '../PortalShell'
import { useDeploymentEvents } from '../useDeploymentEvents'

export interface BssSectionShellProps {
  /** Back-office sub-path under marketplace.<sov-fqdn>/back-office/. */
  path: 'billing' | 'orders' | 'revenue' | 'vouchers' | 'tenants'
  /** Page title shown in the PortalShell header band. */
  title: string
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
}

/**
 * resolveBackOfficeBase — derive the back-office host from
 * DETECTED_MODE.sovereignFQDN. Returns null on Catalyst-Zero
 * (mothership preview) where the marketplace storefront isn't deployed.
 */
export function resolveBackOfficeBase(): string | null {
  const fqdn = DETECTED_MODE.sovereignFQDN
  if (!fqdn) return null
  return `https://marketplace.${fqdn}/back-office`
}

export function BssSectionShell({
  path,
  title,
  disableStream = false,
}: BssSectionShellProps) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN =
    snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const base = resolveBackOfficeBase()
  const src = base ? `${base}/${path}/` : null

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle={title}
      headerSlotLeft={
        <Link
          to={'/bss' as never}
          className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid={`sov-bss-back-to-landing-${path}`}
        >
          ← Back to BSS overview
        </Link>
      }
    >
      {src ? (
        // Allow scripts + same-origin so cookie auth carries; allow
        // forms + popups so order-detail drill-downs and Revenue CSV
        // export work. Match the prior BssIframe attributes verbatim.
        <iframe
          key={src}
          src={src}
          title={title}
          className="h-[calc(100vh-7rem)] w-full border-0 bg-[var(--color-bg)]"
          sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-downloads allow-modals"
          referrerPolicy="same-origin"
          loading="eager"
          data-testid={`sov-bss-iframe-${path}`}
          data-back-office-src={src}
        />
      ) : (
        <div
          className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]"
          data-testid={`sov-bss-no-marketplace-${path}`}
        >
          <p className="font-medium text-[var(--color-text)]">
            BSS is a Sovereign-only surface.
          </p>
          <p className="mt-2">
            Open a deployed Sovereign Console (e.g.&nbsp;
            <span className="font-mono">
              https://console.&lt;your-sov-fqdn&gt;/bss/{path}
            </span>
            ) to manage this section.
          </p>
        </div>
      )}
    </PortalShell>
  )
}
