/**
 * CreateOrganizationPage — operator-facing Organization create form (issue
 * #828, parent epic #825 / Multi-domain Sovereign).
 *
 * The page extends the #804 Organization provisioning pipeline with operator choice
 * over which parent domain hosts the new Organization's free subdomain. A
 * Sovereign at signup brings N parent domains; one is `role: primary`
 * (host of `console.<sovereign>`) and zero-or-more are `role:
 * org-pool` (offered to Organizations). When the operator picks
 * domain_mode=free-subdomain they choose one org-pool parent from the
 * dropdown; the resulting URL is `console.<org-subdomain>.<chosen-parent>`.
 *
 * For domain_mode=byo the operator types the Organization's own apex (e.g.
 * "acme.com") and the back end validates the CNAME points at any
 * parent in the pool.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #2 (never compromise on quality): the page paints partial state
 *      after submit, surfacing the orchestrator's per-step badges so
 *      the operator knows exactly which gate failed.
 *   #4 (never hardcode): every URL flows through apiUrl() in
 *      org.api.ts; the parent-domain pool is wholly data-driven from
 *      the back end.
 */

import { useEffect, useState } from 'react'
import { Globe, ServerCog, Plus } from 'lucide-react'
import {
  createOrganization,
  isParentDomainReady,
  listSovereignParentDomains,
  type OrgProvisionRecord,
  type OrgDomainMode,
  type SovereignParentDomain,
} from './org.api'
// Organizations model (issue #3378 B1) — the kind-derived defaults that
// drive the internal door's billingMode + isolation rendering.
import {
  kindDefaults,
  type OrgBillingMode,
  type OrgIsolation,
  type OrgKind,
} from '@/lib/organizations.api'

export interface CreateOrganizationPageProps {
  /** Test seam — supplies the parent-domain pool synchronously. */
  initialParentDomains?: SovereignParentDomain[]
  /** Test seam — disables the parent-domain fetch effect entirely. */
  disableFetch?: boolean
}

export function CreateOrganizationPage({
  initialParentDomains,
  disableFetch = false,
}: CreateOrganizationPageProps = {}) {
  const [pool, setPool] = useState<SovereignParentDomain[]>(
    initialParentDomains?.filter((p) => p.role === 'org-pool') ?? [],
  )
  const [poolLoading, setPoolLoading] = useState<boolean>(
    !initialParentDomains && !disableFetch,
  )
  const [poolError, setPoolError] = useState<string | null>(null)

  const [subdomain, setSubdomain] = useState<string>('')
  const [companyName, setCompanyName] = useState<string>('')
  const [adminEmail, setAdminEmail] = useState<string>('')
  const [domainMode, setDomainMode] =
    useState<OrgDomainMode>('free-subdomain')
  const [parentDomain, setParentDomain] = useState<string>('')
  const [byoDomain, setByoDomain] = useState<string>('')

  // ── Organizations internal door (issue #3378 B1) ──
  // kind drives the kind-derived billingMode + isolation defaults
  // (internal → showback + namespace; customer → real + vcluster). The
  // advanced override exposes the two derived fields for the rare org
  // that needs a non-default shape (§2.1 advanced-view override). kind
  // defaults to 'customer' so this page behaves exactly as the legacy
  // Organization create form until the operator picks Internal.
  const [orgKind, setOrgKind] = useState<OrgKind>('customer')
  const [advancedOpen, setAdvancedOpen] = useState<boolean>(false)
  const derived = kindDefaults(orgKind)
  const [billingMode, setBillingMode] = useState<OrgBillingMode>(derived.billingMode)
  const [isolation, setIsolation] = useState<OrgIsolation>(derived.isolation)

  // When kind flips, re-seed billingMode + isolation to the kind default
  // UNLESS the operator has opened the advanced override (then their
  // explicit choice sticks). This is what makes "internal → showback +
  // namespace render" the moment Internal is selected (DoD-4).
  useEffect(() => {
    if (advancedOpen) return
    const d = kindDefaults(orgKind)
    setBillingMode(d.billingMode)
    setIsolation(d.isolation)
  }, [orgKind, advancedOpen])

  const isInternal = orgKind === 'internal'

  const [submitting, setSubmitting] = useState<boolean>(false)
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [created, setCreated] = useState<OrgProvisionRecord | null>(null)

  useEffect(() => {
    if (disableFetch) return
    let cancelled = false
    setPoolLoading(true)
    listSovereignParentDomains('org-pool')
      .then((items) => {
        if (cancelled) return
        // Filter to org-pool entries only. The back end's
        // /sovereign/parent-domains endpoint accepts ?role= but also
        // surfaces the implicit primary; the Organization create form is only
        // concerned with role=org-pool entries.
        const orgPool = items.filter((p) => p.role === 'org-pool')
        setPool(orgPool)
        // Default the parent-domain dropdown to the first NS-flip-ready
        // entry. Falls back to the first entry when none are ready (the
        // back end will return 503 Retry-After in that case).
        const firstReady =
          orgPool.find((p) => isParentDomainReady(p)) ?? orgPool[0]
        if (firstReady) setParentDomain(firstReady.name)
        setPoolError(null)
      })
      .catch((err: Error) => {
        if (!cancelled) setPoolError(err.message)
      })
      .finally(() => {
        if (!cancelled) setPoolLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [disableFetch])

  // Pre-select the first parent on initial render when initialParentDomains
  // is supplied (the test seam path).
  useEffect(() => {
    if (initialParentDomains && initialParentDomains.length > 0 && !parentDomain) {
      const orgPool = initialParentDomains.filter((p) => p.role === 'org-pool')
      const firstReady =
        orgPool.find((p) => isParentDomainReady(p)) ?? orgPool[0]
      if (firstReady) setParentDomain(firstReady.name)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialParentDomains])

  const consolePreview =
    domainMode === 'free-subdomain'
      ? subdomain && parentDomain
        ? `console.${subdomain}.${parentDomain}`
        : ''
      : byoDomain
        ? `console.${byoDomain}`
        : ''

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    setSubmitError(null)
    setCreated(null)
    setSubmitting(true)
    try {
      const result = await createOrganization({
        subdomain: subdomain.trim().toLowerCase(),
        admin_email: adminEmail.trim(),
        company_name: companyName.trim(),
        domain_mode: domainMode,
        parent_domain:
          domainMode === 'free-subdomain'
            ? parentDomain.trim().toLowerCase()
            : undefined,
        byo_domain:
          domainMode === 'byo' ? byoDomain.trim().toLowerCase() : undefined,
        // Organizations model (issue #3378 B1) — send the resolved shape.
        // The backend re-derives + validates; we send the explicit values
        // so the advanced override round-trips.
        kind: orgKind,
        billing_mode: billingMode,
        isolation,
      })
      setCreated(result)
    } catch (err) {
      setSubmitError((err as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div data-testid="org-create-page" className="px-6 py-4">
      <div className="mb-4 flex items-center justify-between">
        <div>
          <h1
            data-testid="create-org-title"
            className="text-xl font-semibold text-[var(--color-text-strong)]"
          >
            Create organization
          </h1>
          <p className="text-sm text-[var(--color-text-dim)]">
            {isInternal
              ? 'An internal department — a people, app, and cost boundary on this Sovereign. No marketplace voucher; consumption attributes to the org via showback.'
              : 'A customer organization — provisions its own isolated runtime (per the isolation shown under Defaults, validated server-side at provision time), blueprint charts, DNS, certs, Keycloak realm, and registry entry. Choose a free subdomain under one of this Sovereign’s parent domains or bring an org-owned apex.'}
          </p>
        </div>
      </div>

      {poolError && (
        <div
          data-testid="org-create-pool-error"
          className="mb-4 rounded-md border border-[var(--color-danger)] bg-[var(--color-danger-soft)] p-3 text-sm text-[var(--color-danger)]"
        >
          Failed to load parent-domain pool: {poolError}
        </div>
      )}

      <form
        data-testid="org-create-form"
        onSubmit={onSubmit}
        className="grid max-w-2xl gap-4 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] p-5"
      >
        {/* ── Organizations internal door (issue #3378 B1) ──
            The kind toggle: Internal = a department (this menu's Create),
            Customer = the external door the marketplace funnel uses. The
            kind-derived billingMode + isolation render beneath, with an
            advanced override. */}
        <fieldset className="grid gap-2 text-sm" data-testid="create-org-kind">
          <legend className="text-[var(--color-text-dim)]">Organization kind</legend>
          <div className="flex gap-2">
            {(['internal', 'customer'] as OrgKind[]).map((k) => {
              const active = orgKind === k
              return (
                <button
                  key={k}
                  type="button"
                  data-testid={`create-org-kind-${k}`}
                  aria-pressed={active}
                  onClick={() => setOrgKind(k)}
                  className={`flex-1 rounded-md border px-3 py-2 text-left transition-colors ${
                    active
                      ? 'border-[var(--color-accent)] bg-[var(--color-accent)]/10 text-[var(--color-text-strong)]'
                      : 'border-[var(--color-border)] bg-[var(--color-input)] text-[var(--color-text-dim)] hover:border-[var(--color-accent)]'
                  }`}
                >
                  <span className="block font-medium capitalize">{k}</span>
                  <span className="block text-xs text-[var(--color-text-dim)]">
                    {k === 'internal'
                      ? 'Department — people/app/cost boundary'
                      : 'Customer — externally billed, own isolated runtime'}
                  </span>
                </button>
              )
            })}
          </div>
          {/* Kind-derived defaults — the §2.3 values that render the
              moment Internal/Customer is selected. */}
          <div
            data-testid="create-org-defaults"
            className="flex flex-wrap items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-input)] px-3 py-2 text-xs"
          >
            <span className="text-[var(--color-text-dim)]">Defaults:</span>
            <span
              data-testid="create-org-billing-mode"
              data-mode={billingMode}
              className="rounded-full border border-[var(--color-border)] px-2 py-0.5 font-medium text-[var(--color-text)]"
            >
              billing: {billingMode}
            </span>
            <span
              data-testid="create-org-isolation"
              data-isolation={isolation}
              className="rounded-full border border-[var(--color-border)] px-2 py-0.5 font-medium text-[var(--color-text)]"
            >
              isolation: {isolation}
            </span>
            <button
              type="button"
              data-testid="create-org-advanced-toggle"
              onClick={() => setAdvancedOpen((v) => !v)}
              className="ml-auto text-[var(--color-accent)] hover:underline"
            >
              {advancedOpen ? 'Hide advanced' : 'Advanced override'}
            </button>
          </div>
          {advancedOpen ? (
            <div
              data-testid="create-org-advanced"
              className="grid gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-input)] p-3 sm:grid-cols-2"
            >
              <label className="grid gap-1">
                <span className="text-[var(--color-text-dim)]">Billing mode</span>
                <select
                  data-testid="create-org-billing-select"
                  value={billingMode}
                  onChange={(e) => setBillingMode(e.target.value as OrgBillingMode)}
                  className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1.5"
                >
                  <option value="real">real</option>
                  <option value="chargeback">chargeback</option>
                  <option value="showback">showback</option>
                </select>
              </label>
              <label className="grid gap-1">
                <span className="text-[var(--color-text-dim)]">Isolation</span>
                <select
                  data-testid="create-org-isolation-select"
                  value={isolation}
                  onChange={(e) => setIsolation(e.target.value as OrgIsolation)}
                  className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1.5"
                >
                  <option value="namespace">namespace</option>
                  <option value="vcluster">vcluster</option>
                </select>
              </label>
            </div>
          ) : null}
        </fieldset>

        <label className="grid gap-1 text-sm">
          <span className="text-[var(--color-text-dim)]">
            Organization slug
          </span>
          <input
            data-testid="org-create-subdomain"
            type="text"
            required
            pattern="[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?"
            value={subdomain}
            onChange={(e) => setSubdomain(e.target.value)}
            placeholder="acme"
            className="rounded-md border border-[var(--color-border)] bg-[var(--color-input)] px-3 py-2 text-sm"
          />
          <span className="text-xs text-[var(--color-text-dim)]">
            Used in resource names (namespace and per-Org resources) and the
            URL when the organization picks free-subdomain mode.
          </span>
        </label>

        <label className="grid gap-1 text-sm">
          <span className="text-[var(--color-text-dim)]">Company name</span>
          <input
            data-testid="org-create-company"
            type="text"
            value={companyName}
            onChange={(e) => setCompanyName(e.target.value)}
            placeholder="Acme Corp"
            className="rounded-md border border-[var(--color-border)] bg-[var(--color-input)] px-3 py-2 text-sm"
          />
        </label>

        <label className="grid gap-1 text-sm">
          <span className="text-[var(--color-text-dim)]">Admin email</span>
          <input
            data-testid="org-create-email"
            type="email"
            required
            value={adminEmail}
            onChange={(e) => setAdminEmail(e.target.value)}
            placeholder="admin@acme.com"
            className="rounded-md border border-[var(--color-border)] bg-[var(--color-input)] px-3 py-2 text-sm"
          />
        </label>

        <fieldset className="grid gap-2">
          <legend className="text-sm text-[var(--color-text-dim)]">
            Domain mode
          </legend>
          <label
            className="flex cursor-pointer items-start gap-3 rounded-md border border-[var(--color-border)] p-3 hover:bg-[var(--color-surface-strong,_rgba(255,255,255,0.04))]"
            data-testid="org-create-mode-free"
          >
            <input
              type="radio"
              name="domain-mode"
              value="free-subdomain"
              checked={domainMode === 'free-subdomain'}
              onChange={() => setDomainMode('free-subdomain')}
              className="mt-1"
            />
            <span className="grid gap-1">
              <span className="flex items-center gap-2 text-sm font-medium">
                <Globe className="h-4 w-4" />
                Free subdomain on a Sovereign parent
              </span>
              <span className="text-xs text-[var(--color-text-dim)]">
                Console URL becomes
                <code className="ml-1 rounded bg-[var(--color-input)] px-1">
                  console.&lt;slug&gt;.&lt;parent&gt;
                </code>
                . Pick the parent below.
              </span>
            </span>
          </label>

          <label
            className="flex cursor-pointer items-start gap-3 rounded-md border border-[var(--color-border)] p-3 hover:bg-[var(--color-surface-strong,_rgba(255,255,255,0.04))]"
            data-testid="org-create-mode-byo"
          >
            <input
              type="radio"
              name="domain-mode"
              value="byo"
              checked={domainMode === 'byo'}
              onChange={() => setDomainMode('byo')}
              className="mt-1"
            />
            <span className="grid gap-1">
              <span className="flex items-center gap-2 text-sm font-medium">
                <ServerCog className="h-4 w-4" />
                Bring your own domain
              </span>
              <span className="text-xs text-[var(--color-text-dim)]">
                The organization owns an apex (e.g.
                <code className="mx-1 rounded bg-[var(--color-input)] px-1">
                  acme.com
                </code>
                ) and CNAMEs
                <code className="mx-1 rounded bg-[var(--color-input)] px-1">
                  console.acme.com
                </code>
                at any parent in this Sovereign&apos;s pool.
              </span>
            </span>
          </label>
        </fieldset>

        {domainMode === 'free-subdomain' && (
          <label className="grid gap-1 text-sm" data-testid="org-create-parent-row">
            <span className="text-[var(--color-text-dim)]">Parent domain</span>
            <select
              data-testid="org-create-parent-select"
              value={parentDomain}
              onChange={(e) => setParentDomain(e.target.value)}
              disabled={poolLoading || pool.length === 0}
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-input)] px-3 py-2 text-sm"
              required
            >
              {poolLoading && <option value="">Loading…</option>}
              {!poolLoading && pool.length === 0 && (
                <option value="">
                  No pool parents available — contact the Sovereign
                  operator
                </option>
              )}
              {pool.map((p) => {
                const ready = isParentDomainReady(p)
                return (
                  <option
                    key={p.name}
                    value={p.name}
                    disabled={!ready}
                    data-testid={`org-create-parent-option-${p.name}`}
                  >
                    {p.name}
                    {ready ? '' : ` (${p.flipStatus.replace('-', ' ')})`}
                  </option>
                )
              })}
            </select>
          </label>
        )}

        {domainMode === 'byo' && (
          <label className="grid gap-1 text-sm">
            <span className="text-[var(--color-text-dim)]">Organization apex domain</span>
            <input
              data-testid="org-create-byo"
              type="text"
              required
              value={byoDomain}
              onChange={(e) => setByoDomain(e.target.value)}
              placeholder="acme.com"
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-input)] px-3 py-2 text-sm"
            />
            <span className="text-xs text-[var(--color-text-dim)]">
              Ask the Organization to add a CNAME from
              <code className="mx-1 rounded bg-[var(--color-input)] px-1">
                console.{byoDomain || '<their-domain>'}
              </code>
              to any of this Sovereign&apos;s parent domains in the pool
              before submitting; the orchestrator validates the CNAME
              and refuses to advance until it points here.
            </span>
          </label>
        )}

        <div className="rounded-md border border-dashed border-[var(--color-border)] bg-[var(--color-input)] p-3 text-sm">
          <span className="text-[var(--color-text-dim)]">Console URL preview: </span>
          <code data-testid="org-create-url-preview" className="font-mono text-[var(--color-text-strong)]">
            {consolePreview || '—'}
          </code>
        </div>

        {submitError && (
          <div
            data-testid="org-create-submit-error"
            className="rounded-md border border-[var(--color-danger)] bg-[var(--color-danger-soft)] p-3 text-sm text-[var(--color-danger)]"
          >
            {submitError}
          </div>
        )}

        <button
          type="submit"
          disabled={submitting}
          data-testid="org-create-submit"
          className="inline-flex items-center justify-center gap-2 rounded-md bg-[var(--color-accent)] px-4 py-2 text-sm font-medium text-white disabled:opacity-50"
        >
          <Plus className="h-4 w-4" />
          {submitting ? 'Provisioning…' : 'Create organization'}
        </button>
      </form>

      {created && (
        <div
          data-testid="org-create-result"
          className="mt-6 max-w-2xl rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] p-4"
        >
          <div className="mb-3 text-sm font-medium">
            Provisioning {created.subdomain}
          </div>
          <dl className="mb-3 grid grid-cols-2 gap-2 text-xs">
            <dt className="text-[var(--color-text-dim)]">Console URL</dt>
            <dd className="font-mono">
              <a
                data-testid="org-create-result-console-href"
                href={`https://${created.console_host}`}
                target="_blank"
                rel="noopener noreferrer"
                className="text-[var(--color-accent)] hover:underline"
              >
                {created.console_host}
              </a>
            </dd>
            {created.parent_domain && (
              <>
                <dt className="text-[var(--color-text-dim)]">Parent domain</dt>
                <dd
                  className="font-mono"
                  data-testid="org-create-result-parent"
                >
                  {created.parent_domain}
                </dd>
              </>
            )}
            <dt className="text-[var(--color-text-dim)]">Organization ID</dt>
            <dd className="font-mono">{created.org_tenant_id}</dd>
          </dl>
          <ProvisionSteps steps={created.steps} />
          {created.last_error && (
            <div className="mt-2 text-xs text-[var(--color-danger)]">
              {created.last_error}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function ProvisionSteps({
  steps,
}: {
  steps: OrgProvisionRecord['steps']
}) {
  const items: { key: keyof OrgProvisionRecord['steps']; label: string }[] = [
    { key: 'vcluster', label: 'vCluster' },
    { key: 'bp_charts', label: 'Charts' },
    { key: 'dns', label: 'DNS' },
    { key: 'certs', label: 'Certs' },
    { key: 'keycloak_clients', label: 'Keycloak' },
    { key: 'registry', label: 'Registry' },
  ]
  return (
    <div className="flex flex-wrap items-center gap-2">
      {items.map((s, i) => {
        const state = steps[s.key]
        const cls =
          state === 'done'
            ? 'bg-[var(--color-success,#16a34a)]'
            : state === 'failed'
              ? 'bg-[var(--color-danger,#dc2626)]'
              : 'bg-[var(--color-text-dim,#9ca3af)]'
        return (
          <span key={s.key} className="flex items-center gap-1 text-xs">
            <span
              className={`inline-block h-2 w-2 rounded-full ${cls}`}
              data-testid={`org-create-step-${s.key}-${state}`}
              aria-label={`${s.label}: ${state}`}
            />
            <span>{s.label}</span>
            {i < items.length - 1 && (
              <span className="text-[var(--color-text-dim)]">→</span>
            )}
          </span>
        )
      })}
    </div>
  )
}
