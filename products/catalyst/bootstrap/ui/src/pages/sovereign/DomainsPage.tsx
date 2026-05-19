/**
 * DomainsPage — Sovereign admin parent-domain pool management surface.
 *
 * Refs #1976 (TBD-A64, A65 deferred sub-bug) — cosmetic-guards
 * CANONICAL_SIDEBAR_LABELS asserts the admin sidebar exposes a
 * `Domains` nav item mirrored from
 * core/console/src/components/Sidebar.svelte. This page is the
 * destination for that nav item.
 *
 * Scope: STUB. The real ParentDomain CRD wiring + DNS propagation
 * panels + registrar-token rotation surface is tracked separately
 * (Refs #1830 ParentDomain CRD pool population; #829 parent-domain
 * admin surface). A follow-up issue will replace this stub with the
 * full pool-management UI.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md (honest empty state) — this stub
 * surfaces a clearly-labelled "API pending" message + a
 * `data-testid="pending-api"` hook so the operator sees the surface
 * but cannot mistake it for a wired feature. The stub is NOT hidden
 * behind a null-guard or a feature flag; the route + the chrome are
 * live so the canonical sidebar nav resolves cleanly.
 */

import { PortalShell } from './PortalShell'
import { useDeploymentEvents } from './useDeploymentEvents'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

export interface DomainsPageProps {
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
}

export function DomainsPage({ disableStream = false }: DomainsPageProps = {}) {
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
      pageTitle="Domains"
    >
      <div className="mx-auto max-w-3xl" data-testid="domains-page">
        <header className="mb-4">
          <p className="text-sm text-[var(--color-text-dim)]">
            Manage the parent-domain pool. New tenant subdomains are minted
            against the active pool; rotate registrar credentials and review
            DNS propagation status here.
          </p>
        </header>
        <section
          className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6"
          data-testid="domains-pending-api"
        >
          <h2 className="text-base font-semibold text-[var(--color-text-strong)]">
            API pending
          </h2>
          <p className="mt-2 text-sm text-[var(--color-text-dim)]">
            The ParentDomain CRD pool-management surface is not yet wired in
            this build. The canonical admin entry-point now resolves; full
            functionality ships in a follow-up release (Refs #1830, #829).
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
