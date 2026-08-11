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
                {/* #5817 (UAT row 7) — `enumValue` marks the fields whose value
                    comes from a fixed vocabulary and may be title-cased for
                    display. Slug and Owner are IDENTIFIERS and are never
                    marked, so they render exactly as the model emits them. */}
                <Field label="Slug" value={target.slug} testid={`org-detail-slug`} />
                <Field label="Kind" value={target.kind} testid="org-detail-kind" enumValue />
                <Field label="Tier" value={target.tier} testid="org-detail-tier" enumValue />
                {/* #4292 (UAT row 7) — the PURCHASED plan, beside the tier it
                    is so easily confused with. Tier is the isolation class;
                    this is what the customer bought, and it is what
                    `Isolation` below is derived from — so the page used to
                    show a consequence of the plan without ever naming it.
                    Rendered only when the Org declares one: the sovereign-root
                    row buys nothing, and a blank "Plan —" would invite the
                    same fabricated default the mapper refuses. `s`/`m`/`xl`
                    are a fixed vocabulary, hence enumValue. */}
                {target.plan ? (
                  <Field label="Plan" value={target.plan} testid="org-detail-plan" enumValue />
                ) : null}
                <Field label="Billing mode" value={target.billingMode} testid="org-detail-billing" enumValue />
                {/* #6145 (UAT row 101) — the boundary the org-controller was
                    OBSERVED to have authored, not the one the create door was
                    asked for. An unmeasured Organization renders an em dash;
                    the mapper no longer substitutes 'vcluster', which is how
                    g7freea (host-namespace-backed, no vCluster on the cluster)
                    came to render the same value as a really-vcluster-backed
                    Organization on hw293. */}
                <Field label="Isolation" value={target.isolation || '—'} testid="org-detail-isolation" enumValue />
                <Field label="Status" value={target.status} testid="org-detail-status" enumValue />
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

/**
 * Field — one row of the Org canonical-fields <dl>.
 *
 * #5817 (UAT row 7). This used to apply `capitalize` to EVERY value, which
 * mangles identifiers: the Org slug `uatco` rendered as `Uatco` and the owner
 * `emrah.baysal@openova.io` as `Emrah.baysal@openova.io`. Neither is what the
 * model emits, and row 7's whole assertion is that the detail page shows the
 * CANONICAL fields — a value the operator cannot copy is not canonical. The slug
 * in particular is the join key an operator types into kubectl and the API.
 *
 * The opt-in direction is deliberate. Capitalisation is now requested per call
 * site via `enumValue`, so a field added later renders VERBATIM unless someone
 * states otherwise. If the default went the other way, the next field added
 * would silently inherit the mangling — which is exactly how this shipped. An
 * un-title-cased enum is a cosmetic miss; a mangled identifier is wrong data.
 */
function Field({
  label,
  value,
  testid,
  enumValue,
}: {
  label: string
  value: string
  testid: string
  /** Value comes from a fixed vocabulary (kind/tier/billing/isolation/status)
   *  and may be title-cased for display. Never set this on an identifier. */
  enumValue?: boolean
}) {
  return (
    <div className="flex flex-col">
      <dt className="text-[var(--color-text-dim)]">{label}</dt>
      <dd
        data-testid={testid}
        className={enumValue ? 'capitalize text-[var(--color-text)]' : 'text-[var(--color-text)]'}
      >
        {value}
      </dd>
    </div>
  )
}
