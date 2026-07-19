/**
 * OrganizationDetailPage — /organizations/$org (issue #3378 §4).
 *
 * The org detail surface: the identity card (the CR fields), the mode-
 * aware consumption panel (showback/chargeback orgs), realm/console
 * links, and the `Enter org` button (sub-orgs only — the §2.4 support
 * session; absent on the parent row, "you are already inside it"). The
 * org's people belong on the org's page — the users + roles tabs absorb
 * the legacy users/roles URLs in a follow-on PR on this chain.
 *
 * No bespoke parent-side estate views (§2.4): the org's OWN console (the
 * limited-similar UI served at console.<org>.<domain>) IS the view; this
 * page is the identity + entry surface, not a rebuilt estate read.
 *
 * Resolves the org from the directory feed by slug (rides EXISTING
 * endpoints — listOrganizations composes the parent + sub-org rows).
 */

import { useMemo } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { PortalShell } from '../PortalShell'
import { ShowbackPanel } from './ShowbackPanel'
import { EnterOrgButton } from './EnterOrgButton'
import { listOrganizations, type OrgRow } from '@/lib/organizations.api'

export interface OrganizationDetailPageProps {
  /** The org slug from the route param. */
  org: string
  /** Test seam — supply the directory rows synchronously. */
  initialOrgsOverride?: readonly OrgRow[]
}

export function OrganizationDetailPage({ org, initialOrgsOverride }: OrganizationDetailPageProps) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''
  const sovereignFQDN = DETECTED_MODE.sovereignFQDN ?? null

  const query = useQuery<OrgRow[]>({
    queryKey: ['organizations-directory', deploymentId],
    queryFn: listOrganizations,
    staleTime: 30_000,
    enabled: !initialOrgsOverride,
    placeholderData: (prev) => prev,
  })
  const rows: readonly OrgRow[] = initialOrgsOverride ?? query.data ?? []
  const loading = !initialOrgsOverride && query.isLoading

  const target = useMemo(() => rows.find((o) => o.slug === org), [rows, org])

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle={target?.displayName ?? org}
      headerSlotLeft={
        <Link
          to={'/organizations' as never}
          className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="org-detail-back-link"
        >
          ← Organizations
        </Link>
      }
      headerSlotRight={
        // Enter org — sub-orgs only (§2.4 + §5: hidden on the parent row,
        // "you are already inside it").
        target && !target.isParent ? <EnterOrgButton org={target} /> : null
      }
    >
      <div data-testid="organization-detail-page" data-org={org}>
        {loading ? (
          <div data-testid="org-detail-loading" className="text-sm text-[var(--color-text-dim)]">Loading…</div>
        ) : !target ? (
          <div data-testid="org-detail-not-found" className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-4 py-8 text-center text-sm text-[var(--color-text-dim)]">
            Organization “{org}” not found in this Sovereign’s directory.
          </div>
        ) : (
          <div className="grid gap-4">
            {/* Identity card — the CR fields (§4). */}
            <section
              data-testid="org-detail-identity"
              className="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4"
            >
              <div className="mb-3 flex items-center gap-2">
                <h2 className="text-sm font-semibold text-[var(--color-text-strong)]">{target.displayName}</h2>
                {target.isParent ? (
                  <span data-testid="org-detail-parent-badge" className="rounded-full border border-[var(--color-accent)]/40 bg-[var(--color-accent)]/10 px-2 py-0.5 text-[9px] font-semibold uppercase tracking-wide text-[var(--color-accent)]">Parent</span>
                ) : null}
              </div>
              <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-xs sm:grid-cols-3">
                <Field label="Slug" value={target.slug} testid={`org-detail-slug`} />
                <Field label="Kind" value={target.kind} testid="org-detail-kind" />
                <Field label="Tier" value={target.tier} testid="org-detail-tier" />
                <Field label="Billing mode" value={target.billingMode} testid="org-detail-billing" />
                <Field label="Isolation" value={target.isolation} testid="org-detail-isolation" />
                <Field label="Status" value={target.status} testid="org-detail-status" />
                {target.ownerEmail ? <Field label="Owner" value={target.ownerEmail} testid="org-detail-owner" /> : null}
                {target.consoleHost ? (
                  <div className="flex flex-col">
                    <dt className="text-[var(--color-text-dim)]">Console</dt>
                    <dd>
                      <a
                        href={`https://${target.consoleHost}`}
                        target="_blank"
                        rel="noreferrer"
                        data-testid="org-detail-console-link"
                        className="font-mono text-[var(--color-accent)] hover:underline"
                      >
                        {target.consoleHost}
                      </a>
                    </dd>
                  </div>
                ) : null}
              </dl>
            </section>

            {/* Consumption panel — mode-aware (showback/chargeback). The
                org's slice of the showback feed. */}
            <ShowbackPanel org={target.isParent ? undefined : target.slug} />
          </div>
        )}
      </div>
    </PortalShell>
  )
}

function Field({ label, value, testid }: { label: string; value: string; testid: string }) {
  return (
    <div className="flex flex-col">
      <dt className="text-[var(--color-text-dim)]">{label}</dt>
      <dd data-testid={testid} className="capitalize text-[var(--color-text)]">{value}</dd>
    </div>
  )
}
