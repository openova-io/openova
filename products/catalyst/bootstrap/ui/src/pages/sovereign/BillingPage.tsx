/**
 * BillingPage — Sovereign admin billing oversight surface.
 *
 * Refs #1976 (TBD-A64, A65 deferred sub-bug) — cosmetic-guards
 * CANONICAL_SIDEBAR_LABELS asserts the admin sidebar exposes a
 * `Billing` nav item mirrored from
 * core/console/src/components/Sidebar.svelte. This page is the
 * destination for that nav item.
 *
 * Scope: STUB. The real billing surface lives under the Sovereign
 * Console chroot at /bss/billing (Family F, Wave 3). This page is the
 * deployment-scoped landing for billing oversight from the catalyst
 * admin sidebar; the actual invoice/payment-method/usage panels are
 * tracked as a follow-up issue.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md (honest empty state) — this stub
 * surfaces a clearly-labelled "API pending" message + a
 * `data-testid="pending-api"` hook so the operator sees the surface
 * but cannot mistake it for a wired feature.
 */

import { PortalShell } from './PortalShell'
import { useDeploymentEvents } from './useDeploymentEvents'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

export interface BillingPageProps {
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
}

export function BillingPage({ disableStream = false }: BillingPageProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN =
    snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Billing"
    >
      <div className="mx-auto max-w-3xl" data-testid="billing-page">
        <header className="mb-4">
          <p className="text-sm text-[var(--color-text-dim)]">
            Review invoices, payment methods, and usage for this Sovereign.
          </p>
        </header>
        <section
          className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6"
          data-testid="billing-pending-api"
        >
          <h2 className="text-base font-semibold text-[var(--color-text-strong)]">
            API pending
          </h2>
          <p className="mt-2 text-sm text-[var(--color-text-dim)]">
            The deployment-scoped billing surface is not yet wired in this
            build. Full BSS billing surfaces live under the Sovereign Console
            chroot at <code>/bss/billing</code>; the deployment-scoped
            invoice/usage panels ship in a follow-up release.
          </p>
          <p
            className="mt-3 text-xs text-[var(--color-text-dim)]"
            data-testid="pending-api"
          >
            pending-api
          </p>
        </section>
      </div>
    </PortalShell>
  )
}
