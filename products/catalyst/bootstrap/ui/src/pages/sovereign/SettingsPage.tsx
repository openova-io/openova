/**
 * SettingsPage — Sovereign-portal admin settings surface (issue #516).
 *
 * Founder ask (verbatim, condensed):
 *   "currently setting button diverting user back to wizard, he is
 *    supposed to see all relevant settings related information
 *    permanently in the settings page, you decide what are supposed to
 *    be there in the settings page such as api key, company names etc
 *    etc, by following the UX industry standards as the UX guru…"
 *
 * Industry-standard SaaS-admin settings sections (GitLab Group Settings,
 * GitHub Org Settings, Vercel Project Settings, Stripe Dashboard):
 *
 *   1. Organization      — name, billing email, logo
 *   2. Sovereign         — FQDN, region, control-plane size, deployment
 *                          id, creation date
 *   3. API tokens        — list / create / revoke (catalyst-api,
 *                          openbao, harbor, gitea, keycloak)
 *   4. Cloud credentials — Hetzner token (masked), S3 keys (masked)
 *   5. DNS               — pool domain, pool subdomain, TLS issuer
 *   6. Domain mode       — pool vs BYO (read-only after activation)
 *   7. Notifications     — email / Slack hooks
 *   8. Members           — operator roles (link to existing User Access)
 *   9. Danger zone       — wipe / decommission / transfer ownership
 *
 * Layout contract (mirrors GitLab/GitHub):
 *   • PortalShell (same sidebar + top header band as Apps / Jobs / etc)
 *   • Two-column body: in-page left rail (TOC) + right pane (sections
 *     stacked vertically, anchor-scrolled)
 *   • Each section is a card with a title row, a short description, and
 *     a focused form / readonly grid
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall, target-state shape
 * first time) — this page surfaces ALL nine sections on first paint.
 * Sections that don't yet have a backend API are clearly labelled with
 * a `pending-api` testid so the operator sees the surface but can't
 * fool themselves into thinking it's wired.
 *
 * Per #2 (no compromise) — the read-only sections (Sovereign, DNS,
 * Domain mode) are wired against the live `useDeploymentEvents`
 * snapshot + the `useWizardStore` so the data on screen is the actual
 * data the deployment was provisioned with, not a placeholder.
 *
 * Per #4 (never hardcode) — every label, every value comes from
 * runtime state. The token-kind list is the only static catalogue and
 * lives in a named constant so it's swappable.
 *
 * Per #10 (credential hygiene) — secrets are NEVER printed in plain
 * form. The Hetzner token / S3 keys / API tokens render as a fixed
 * `••••••••` mask with a "Rotate" affordance; the actual value is
 * never read into the DOM.
 */

import { useMemo } from 'react'
import { Link, useParams } from '@tanstack/react-router'

import { PortalShell } from './PortalShell'
import { useDeploymentEvents } from './useDeploymentEvents'
import { useWizardStore } from '@/entities/deployment/store'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { MarketplaceSection } from './settings/MarketplaceSection'
import { ParentDomainsSection } from '@/pages/admin/parent-domains/ParentDomainsSection'
import { SovereigntyCard } from '@/widgets/sovereignty'

/* ── Constants ──────────────────────────────────────────────────── */

/**
 * The catalyst-side API tokens a Sovereign exposes. Each Sovereign
 * provisions credentials for these five services as part of bootstrap.
 * The list is intentionally explicit — it's the canonical surface of
 * the OpenOva control plane.
 */
const API_TOKEN_KINDS = [
  { id: 'catalyst-api', label: 'Catalyst API', scope: 'Sovereign control plane' },
  { id: 'openbao', label: 'OpenBao', scope: 'Secrets vault' },
  { id: 'harbor', label: 'Harbor', scope: 'Container registry' },
  { id: 'gitea', label: 'Gitea', scope: 'GitOps source' },
  { id: 'keycloak', label: 'Keycloak', scope: 'Identity provider' },
] as const

/** In-page section catalogue — drives the left rail and the heading on
 *  each card so the two never drift apart. */
interface SectionDef {
  id: string
  label: string
  description: string
}

const SECTIONS: readonly SectionDef[] = [
  { id: 'organization', label: 'Organization', description: 'Organisation profile, billing contact, logo.' },
  { id: 'sovereign', label: 'Sovereign', description: 'FQDN, region, Capacity (control plane sizing), deployment id, creation date.' },
  { id: 'api-tokens', label: 'API tokens', description: 'List, create, and revoke service tokens.' },
  { id: 'cloud-credentials', label: 'Cloud credentials', description: 'Hetzner provider token + S3 backup keys.' },
  { id: 'dns', label: 'DNS', description: 'Pool domain, subdomain, TLS issuer status.' },
  { id: 'domain-mode', label: 'Domain mode', description: 'Pool vs Bring-Your-Own — read-only after activation.' },
  // #4089 (founder UX-polish): Parent Domains was the lone child in
  // SovereignSidebar's SETTINGS_SUB_NAV — the only odd-one-out sub-left-
  // pane menu item. Re-homed as a granular `<SectionCard id="parent-
  // domains">` anchor section here, consistent with #dns and the move
  // Marketplace already got (Wave 5). Sits right after Domain mode since
  // both are domain-config surfaces. The standalone page lives on at
  // /organizations/domains (the same ParentDomainsSection in a
  // PortalShell), and /parent-domains still redirects there.
  { id: 'parent-domains', label: 'Parent domains', description: 'The pool of registrar domains this Sovereign serves via PowerDNS — add / remove pool domains, watch NS-flip + DNS propagation.' },
  // Wave 5 (2026-05-17, founder UX-polish review): marketplace toggle
  // moved off the Settings sub-nav + standalone /settings/marketplace
  // page INTO this anchor section. Founder ruling: *"if market place
  // is just a toggle etting under setting it dosnt need tohave a
  // sdicated page ... it shoudl be somewher e here ... similar to
  // other setting"*.
  { id: 'marketplace', label: 'Marketplace', description: 'Public storefront, branding, Organization wildcard ingress. Changes are committed to your GitOps repo and reconciled by Flux within ~1 minute.' },
  { id: 'notifications', label: 'Notifications', description: 'Email + Slack hooks for provisioning events.' },
  { id: 'members', label: 'Members', description: 'Operators with admin / dev / viewer roles.' },
  { id: 'danger-zone', label: 'Danger zone', description: 'Wipe Sovereign, decommission, transfer ownership.' },
  // #793 production mount: the cutover ("Achieve True Sovereignty") card
  // previously existed ONLY on the layout-free /sovereignty/preview harness
  // route, so the operator console had no production surface to start the
  // sovereignty cutover (UAT TC-24 ❌). Appended last so existing SECTIONS[N]
  // index references stay stable.
  { id: 'sovereignty', label: 'Sovereignty', description: 'Achieve true sovereign independence — sever the mothership tethers via the cutover.' },
] as const

const TOKEN_MASK = '••••••••••••••••'

/** Look a section's description up by id so the render code never has to
 *  track fragile numeric `SECTIONS[N]` offsets when sections are inserted
 *  (e.g. #4089 added `parent-domains` mid-array). */
function desc(id: string): string {
  return SECTIONS.find((s) => s.id === id)?.description ?? ''
}

/* ── Page ───────────────────────────────────────────────────────── */

export interface SettingsPageProps {
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
}

export function SettingsPage({ disableStream = false }: SettingsPageProps = {}) {
  // Use strict:false params + useResolvedDeploymentId so this page
  // works on BOTH the mother route (/provision/$deploymentId/settings)
  // AND the chroot Sovereign route (/settings). Strict-mode useParams
  // throws Invariant on the chroot route since the path doesn't match.
  // Caught on omantel.biz 2026-05-06.
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? resolvedId ?? ''
  // Which of the two routes are we actually mounted on? The presence of a
  // `:deploymentId` PATH PARAM is the only honest signal — `deploymentId`
  // above falls back to the resolver, which answers on both routes. Links
  // to sibling pages must be built from this, because the mothership
  // routes are `/provision/$deploymentId/*` while the chroot Sovereign
  // serves the same pages at the domain root.
  const scopedToProvisionRoute = Boolean(params.deploymentId)

  const store = useWizardStore()

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })

  // Resolve the shape the page needs from the live snapshot. Each value
  // is derived defensively — a not-yet-finished deployment has no
  // sovereignFQDN; we render an em-dash placeholder so the page is
  // still complete on first paint.
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null
  const region = snapshot?.region ?? null
  const startedAt = snapshot?.startedAt ?? null
  const status = snapshot?.status ?? null

  // C8-001 (2026-05-17 t143): prefer the live snapshot for the
  // Sovereign + DNS fields, fall back to the wizard store. The chroot
  // Sovereign console has a fresh localStorage (the wizard runs on
  // mothership, the chroot session never persists the store), so
  // wizard-store-only fields rendered four em-dashes for Capacity /
  // Pool subdomain / BYO domain / CP size. catalyst-api's
  // Deployment.State() now surfaces these from the persisted
  // RedactedRequest projection — they're the authoritative source on
  // every Sovereign post-handover. The wizard-store fallback covers
  // the mothership wizard-in-flight case where the snapshot may not
  // yet carry the request fields (pre-CreateDeployment).
  const poolDomain = snapshot?.sovereignPoolDomain ?? store.sovereignPoolDomain ?? null
  const poolSubdomain = snapshot?.sovereignSubdomain ?? store.sovereignSubdomain ?? null
  const domainMode = snapshot?.sovereignDomainMode ?? store.sovereignDomainMode ?? null
  const byoDomain = snapshot?.sovereignByoDomain ?? store.sovereignByoDomain ?? null
  const orgName = snapshot?.orgName ?? store.orgName ?? null
  const orgEmail = snapshot?.orgEmail ?? store.orgEmail ?? null
  // OrgIndustry / OrgHeadquarters are wizard-store-only fields today —
  // not persisted on the deployment record. They render the em-dash
  // placeholder on the chroot until a future PR plumbs them through
  // the provisioner.Request payload.
  const orgIndustry = store.orgIndustry || null
  const orgHeadquarters = store.orgHeadquarters || null

  // Control-plane size: per-region SKUs are an array; surface region 0
  // since the founder spec is single-region happy path. The full per-
  // region table belongs on a future Compute settings sub-page.
  const controlPlaneSize = useMemo(() => {
    // Prefer snapshot (chroot Sovereign source-of-truth). Multi-region
    // arrays surface from snapshot.regionControlPlaneSizes; single
    // region from snapshot.controlPlaneSize. Falls back to wizard
    // store for the mothership wizard-in-flight case.
    const snapArr = snapshot?.regionControlPlaneSizes
    if (Array.isArray(snapArr) && snapArr.length > 0 && snapArr[0]) return snapArr[0]
    if (snapshot?.controlPlaneSize) return snapshot.controlPlaneSize
    const storeArr = store.regionControlPlaneSizes
    if (Array.isArray(storeArr) && storeArr.length > 0 && storeArr[0]) return storeArr[0]
    if (store.controlPlaneSize) return store.controlPlaneSize
    return null
  }, [
    snapshot?.regionControlPlaneSizes,
    snapshot?.controlPlaneSize,
    store.regionControlPlaneSizes,
    store.controlPlaneSize,
  ])

  return (
    <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN} pageTitle="Settings">
      <div className="mx-auto max-w-7xl" data-testid="settings-page">
        {/* #531 item 6: founder-mandated narrower left rail (~180px).
            The previous col-span-3 grid carved out ~25% of the page
            width for nine short labels; flex with a fixed-width aside
            keeps the rail compact while letting the right pane consume
            the remaining width. */}
        <div className="flex flex-col gap-6 sm:flex-row">
          {/* ── In-page left rail ─────────────────────────────── */}
          <aside
            className="w-full shrink-0 sm:w-[180px]"
            data-testid="settings-toc"
          >
            <nav className="sticky top-20 flex flex-col gap-1 text-sm">
              {SECTIONS.map((s) => (
                <a
                  key={s.id}
                  href={`#${s.id}`}
                  data-testid={`settings-toc-${s.id}`}
                  className="rounded-md px-3 py-2 text-[var(--color-text-dim)] no-underline transition-colors hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]"
                >
                  {s.label}
                </a>
              ))}
            </nav>
          </aside>

          {/* ── Right pane — sections stacked ─────────────────── */}
          <main className="flex min-w-0 flex-1 flex-col gap-6">
            {/* 1. Organization */}
            <SectionCard id="organization" title="Organization" description={desc('organization')}>
              <FieldGrid>
                <Field label="Name" value={orgName} testId="settings-org-name" />
                <Field label="Billing email" value={orgEmail} testId="settings-org-email" />
                <Field label="Industry" value={orgIndustry} testId="settings-org-industry" />
                <Field label="Headquarters" value={orgHeadquarters} testId="settings-org-hq" />
              </FieldGrid>
            </SectionCard>

            {/* 2. Sovereign */}
            <SectionCard id="sovereign" title="Sovereign" description={desc('sovereign')}>
              <FieldGrid>
                <Field label="Sovereign FQDN" value={sovereignFQDN} mono testId="settings-sov-fqdn" />
                <Field label="Region" value={region} mono testId="settings-sov-region" />
                <Field
                  label="Capacity"
                  value={controlPlaneSize}
                  mono
                  testId="settings-sov-capacity"
                />
                <Field
                  label="Control plane size"
                  value={controlPlaneSize}
                  mono
                  testId="settings-sov-cp-size"
                />
                <Field label="Deployment ID" value={deploymentId} mono testId="settings-sov-deployment-id" />
                <Field
                  label="Created"
                  value={startedAt ? new Date(startedAt).toLocaleString() : null}
                  testId="settings-sov-created"
                />
                <Field label="Status" value={status} testId="settings-sov-status" />
              </FieldGrid>
            </SectionCard>

            {/* 3. API tokens */}
            <SectionCard
              id="api-tokens"
              title="API tokens"
              description={desc('api-tokens')}
              actionLabel="Create token"
              actionTestId="settings-tokens-create"
              pendingApi
            >
              <ul className="flex flex-col gap-2" data-testid="settings-tokens-list">
                {API_TOKEN_KINDS.map((t) => (
                  <li
                    key={t.id}
                    data-testid={`settings-token-row-${t.id}`}
                    className="flex items-center justify-between rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2"
                  >
                    <div className="flex flex-col">
                      <span className="text-sm font-medium text-[var(--color-text-strong)]">
                        {t.label}
                      </span>
                      <span className="text-xs text-[var(--color-text-dim)]">{t.scope}</span>
                    </div>
                    <div className="flex items-center gap-3">
                      <code className="font-mono text-xs text-[var(--color-text-dim)]">{TOKEN_MASK}</code>
                      <button
                        type="button"
                        disabled
                        title="Token rotation API not yet wired"
                        className="rounded-md border border-[var(--color-border)] px-2 py-1 text-xs text-[var(--color-text-dim)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] disabled:opacity-50 disabled:cursor-not-allowed"
                        data-testid={`settings-token-revoke-${t.id}`}
                      >
                        Revoke
                      </button>
                    </div>
                  </li>
                ))}
              </ul>
            </SectionCard>

            {/* 4. Cloud credentials */}
            <SectionCard
              id="cloud-credentials"
              title="Cloud credentials"
              description={desc('cloud-credentials')}
              pendingApi
            >
              <ul className="flex flex-col gap-2" data-testid="settings-cloud-creds-list">
                <CredRow id="hetzner-token" label="Hetzner provider token" />
                <CredRow id="s3-access-key" label="S3 backup access key" />
                <CredRow id="s3-secret-key" label="S3 backup secret key" />
              </ul>
            </SectionCard>

            {/* 5. DNS */}
            <SectionCard id="dns" title="DNS" description={desc('dns')}>
              <FieldGrid>
                <Field label="Pool domain" value={poolDomain} mono testId="settings-dns-pool-domain" />
                <Field
                  label="Pool subdomain"
                  value={poolSubdomain}
                  mono
                  testId="settings-dns-pool-subdomain"
                />
                <Field label="BYO domain" value={byoDomain} mono testId="settings-dns-byo-domain" />
                <Field
                  label="TLS issuer"
                  value={sovereignFQDN ? 'Let’s Encrypt (cert-manager)' : null}
                  testId="settings-dns-tls-issuer"
                />
              </FieldGrid>
            </SectionCard>

            {/* 6. Domain mode */}
            <SectionCard id="domain-mode" title="Domain mode" description={desc('domain-mode')}>
              <FieldGrid>
                <Field label="Mode" value={domainMode} testId="settings-domain-mode-value" />
                <Field
                  label="Lock state"
                  value={sovereignFQDN ? 'Locked (Sovereign activated)' : 'Editable (pre-activation)'}
                  testId="settings-domain-mode-lock"
                />
              </FieldGrid>
            </SectionCard>

            {/* 7. Parent domains — #4089 (2026-06-22): re-homed from the
                lone Settings sub-nav child / standalone /parent-domains
                route into this anchor section, consistent with #dns +
                the move Marketplace already got. Same ParentDomainsSection
                the standalone /organizations/domains page renders. */}
            <SectionCard id="parent-domains" title="Parent domains" description={desc('parent-domains')}>
              <ParentDomainsSection />
            </SectionCard>

            {/* 8. Marketplace — Wave 5 (2026-05-17): moved here from
                the retired /settings/marketplace standalone page +
                Settings sub-nav child. */}
            <SectionCard id="marketplace" title="Marketplace" description={desc('marketplace')}>
              <MarketplaceSection />
            </SectionCard>

            {/* 9. Notifications */}
            <SectionCard
              id="notifications"
              title="Notifications"
              description={desc('notifications')}
              pendingApi
            >
              <FieldGrid>
                <Field label="Email recipients" value={null} testId="settings-notif-email" />
                <Field label="Slack webhook" value={null} testId="settings-notif-slack" />
              </FieldGrid>
            </SectionCard>

            {/* 10. Members — link to existing User Access page */}
            <SectionCard id="members" title="Members" description={desc('members')}>
              <p className="text-sm text-[var(--color-text-dim)]">
                Operators are managed on the dedicated User Access page so role bindings, app
                grants, and namespace scopes share one editor.
              </p>
              {/* Both User Access routes are registered in the one tree:
                  `/provision/$deploymentId/users` (mothership) and the
                  chroot `/users` under consoleLayoutRoute. This link used
                  to hardcode the chroot form for both, so on the
                  mothership it navigated into SovereignConsoleLayout with
                  no :deploymentId param, where useResolvedDeploymentId
                  falls back to /sovereign/self — which 404s on a
                  mothership host. Members was a dead link there. Pick the
                  form that matches the route we are mounted on rather
                  than casting the target past the router's typed-route
                  check. */}
              {scopedToProvisionRoute ? (
                <Link
                  to="/provision/$deploymentId/users"
                  params={{ deploymentId }}
                  className="mt-2 inline-flex items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm text-[var(--color-text)] no-underline hover:border-[var(--color-accent)] hover:text-[var(--color-accent)]"
                  data-testid="settings-members-link"
                >
                  Open User Access →
                </Link>
              ) : (
                <Link
                  to="/users"
                  className="mt-2 inline-flex items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm text-[var(--color-text)] no-underline hover:border-[var(--color-accent)] hover:text-[var(--color-accent)]"
                  data-testid="settings-members-link"
                >
                  Open User Access →
                </Link>
              )}
            </SectionCard>

            {/* 11. Danger zone */}
            <SectionCard
              id="danger-zone"
              title="Danger zone"
              description={desc('danger-zone')}
              tone="danger"
            >
              <ul className="flex flex-col gap-3">
                <DangerRow
                  testId="settings-danger-decommission"
                  title="Decommission Sovereign"
                  body="Tears down every cloud resource provisioned for this Sovereign. Irreversible."
                  cta={
                    <Link
                      to="/decommission/$deploymentId"
                      params={{ deploymentId }}
                      className="rounded-md border border-rose-500/60 bg-rose-500/5 px-3 py-2 text-sm text-rose-300 no-underline hover:bg-rose-500/15"
                      data-testid="settings-danger-decommission-link"
                    >
                      Decommission…
                    </Link>
                  }
                />
                <DangerRow
                  testId="settings-danger-wipe"
                  title="Wipe Sovereign data"
                  body="Wipes deployment state but keeps the cloud account. Used when the Sovereign got into a bad state and you want to reprovision from the wizard."
                  pendingApi
                />
                <DangerRow
                  testId="settings-danger-transfer"
                  title="Transfer ownership"
                  body="Hands Sovereign administration to a new operator account."
                  pendingApi
                />
              </ul>
            </SectionCard>

            {/* 12. Sovereignty — #793 production mount of the cutover card
                (was only reachable at the /sovereignty/preview harness route).
                Self-contained widget: renders its own header/badge/CTA and
                self-fetches cutover status, so it is not wrapped in a
                SectionCard. */}
            <div id="sovereignty" className="scroll-mt-20" data-testid="settings-sovereignty">
              <SovereigntyCard />
            </div>
          </main>
        </div>
      </div>
    </PortalShell>
  )
}

/* ── Reusable presentation primitives ───────────────────────────── */

interface SectionCardProps {
  id: string
  title: string
  description: string
  children: React.ReactNode
  /** Optional CTA shown on the section header right side. */
  actionLabel?: string
  actionTestId?: string
  /** When true, render an "API pending" pill so the operator knows the
   *  surface exists but the backing API is not yet wired. Drives a
   *  `data-pending-api` attribute the test suite asserts on. */
  pendingApi?: boolean
  /** Tone — danger sections render with a rose border. */
  tone?: 'default' | 'danger'
}

function SectionCard({
  id,
  title,
  description,
  children,
  actionLabel,
  actionTestId,
  pendingApi,
  tone = 'default',
}: SectionCardProps) {
  const borderClass =
    tone === 'danger'
      ? 'border-rose-500/40 bg-rose-500/5'
      : 'border-[var(--color-border)] bg-[var(--color-bg-2)]'
  return (
    <section
      id={id}
      data-testid={`settings-section-${id}`}
      data-pending-api={pendingApi ? 'true' : undefined}
      className={`scroll-mt-20 rounded-xl border ${borderClass} p-5`}
    >
      <header className="mb-4 flex items-start justify-between gap-3">
        <div>
          <h2
            data-testid={`settings-section-title-${id}`}
            className="text-base font-semibold text-[var(--color-text-strong)]"
          >
            {title}
          </h2>
          <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">{description}</p>
        </div>
        <div className="flex items-center gap-2">
          {pendingApi && (
            <span
              data-testid={`settings-pending-api-${id}`}
              className="rounded-full border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide text-amber-300"
              title="Backend API not yet wired — display only"
            >
              API pending
            </span>
          )}
          {actionLabel && (
            <button
              type="button"
              disabled={pendingApi}
              data-testid={actionTestId}
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-1.5 text-xs text-[var(--color-text)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {actionLabel}
            </button>
          )}
        </div>
      </header>
      {children}
    </section>
  )
}

function FieldGrid({ children }: { children: React.ReactNode }) {
  return <dl className="grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-2">{children}</dl>
}

function Field({
  label,
  value,
  mono,
  testId,
}: {
  label: string
  value: string | null | undefined
  mono?: boolean
  testId?: string
}) {
  const isEmpty = !value
  const valueClass = `mt-0.5 truncate text-sm ${
    isEmpty ? 'text-[var(--color-text-dimmer)]' : 'text-[var(--color-text)]'
  } ${mono ? 'font-mono' : ''}`
  return (
    <div data-testid={testId}>
      <dt className="text-xs font-medium text-[var(--color-text-dim)]">{label}</dt>
      <dd className={valueClass}>{isEmpty ? '—' : value}</dd>
    </div>
  )
}

function CredRow({ id, label }: { id: string; label: string }) {
  return (
    <li
      data-testid={`settings-cred-row-${id}`}
      className="flex items-center justify-between rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2"
    >
      <span className="text-sm text-[var(--color-text)]">{label}</span>
      <div className="flex items-center gap-3">
        <code className="font-mono text-xs text-[var(--color-text-dim)]">{TOKEN_MASK}</code>
        <button
          type="button"
          disabled
          title="Cloud credential rotation API not yet wired"
          className="rounded-md border border-[var(--color-border)] px-2 py-1 text-xs text-[var(--color-text-dim)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] disabled:opacity-50 disabled:cursor-not-allowed"
          data-testid={`settings-cred-rotate-${id}`}
        >
          Rotate
        </button>
      </div>
    </li>
  )
}

function DangerRow({
  testId,
  title,
  body,
  cta,
  pendingApi,
}: {
  testId: string
  title: string
  body: string
  cta?: React.ReactNode
  pendingApi?: boolean
}) {
  return (
    <li
      data-testid={testId}
      data-pending-api={pendingApi ? 'true' : undefined}
      className="flex items-start justify-between gap-4 rounded-md border border-rose-500/30 bg-[var(--color-bg)] p-3"
    >
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium text-rose-200">{title}</p>
        <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">{body}</p>
      </div>
      <div className="flex items-center gap-2">
        {pendingApi && (
          <span
            data-testid={`${testId}-pending`}
            className="rounded-full border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide text-amber-300"
          >
            API pending
          </span>
        )}
        {cta}
      </div>
    </li>
  )
}
