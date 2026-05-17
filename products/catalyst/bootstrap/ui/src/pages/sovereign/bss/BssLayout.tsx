/**
 * BssLayout — shared chrome for /console/bss/* (founder #1 requirement).
 *
 * Founder ruling (2026-05-17, family-F brief):
 *   "the backed of the the mark place mutst be just aotnerh menu under
 *    console like https://console.<sov>/bss"
 *   "it is just matter of roles based access ... where we give the
 *    billing access they see the billign etc."
 *
 * Architecture (option B — iframe embed):
 *
 *   Public URL the operator sees   : console.<sov-fqdn>/bss/<section>
 *   Iframe src (server-rendered UI): https://marketplace.<sov-fqdn>/back-office/<section>/
 *
 * The admin Pod in the sme namespace (chart template
 * templates/sme-services/admin.yaml) already serves the BSS UI on the
 * marketplace HTTPRoute's /back-office/ prefix; this layout keeps the
 * Sovereign Console URL canonical while reusing the existing,
 * production-ready back-office surfaces (billing, orders, revenue,
 * vouchers, tenants).
 *
 * Why iframe and not a fresh port:
 *
 *   1. The back-office is its own SPA + service surface; re-implementing
 *      5 admin pages in React would be a ~3k-line port that drifts away
 *      from the canonical Astro storefront. Iframe is the lowest-drift
 *      shipping pattern.
 *   2. Authentication: same-origin cookies on `*.<sov-fqdn>` cover the
 *      iframe's cross-subdomain XHR. The admin Pod still gates on the
 *      SME gateway's session.
 *   3. RBAC gating happens at TWO layers — sidebar visibility (this PR;
 *      always-on for v1 since the whoami payload doesn't yet expose
 *      tier — pattern matches /rbac/* and /sre/compliance which are
 *      similarly unconditional today) and the SME gateway's
 *      session-tier check on /back-office/* requests (already shipped).
 *      When whoami grows a `tier` field we add a beforeLoad gate.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode): the back-office
 * host is derived from `DETECTED_MODE.sovereignFQDN` at runtime, never
 * baked at build time.
 */

import { Link, Outlet, useRouterState } from '@tanstack/react-router'
import { DETECTED_MODE } from '@/shared/lib/detectMode'

interface BssTab {
  id: 'billing' | 'orders' | 'revenue' | 'vouchers' | 'tenants'
  label: string
  to: string
  /** Sub-path under the admin Pod's `/back-office/` mount. */
  backOfficePath: string
}

export const BSS_TABS: BssTab[] = [
  { id: 'billing', label: 'Billing', to: '/bss/billing', backOfficePath: 'billing' },
  { id: 'orders', label: 'Orders', to: '/bss/orders', backOfficePath: 'orders' },
  { id: 'revenue', label: 'Revenue', to: '/bss/revenue', backOfficePath: 'revenue' },
  { id: 'vouchers', label: 'Vouchers', to: '/bss/vouchers', backOfficePath: 'vouchers' },
  { id: 'tenants', label: 'Tenants', to: '/bss/tenants', backOfficePath: 'tenants' },
]

function deriveActiveTab(pathname: string): BssTab['id'] | null {
  for (const tab of BSS_TABS) {
    if (pathname === tab.to || pathname.startsWith(tab.to + '/')) return tab.id
  }
  return null
}

/**
 * Resolve the back-office base URL.
 *
 * On Sovereign clusters (console.<sov-fqdn>) the marketplace storefront
 * sits at marketplace.<sov-fqdn>. On Catalyst-Zero (contabo) the
 * marketplace isn't deployed — render an explanatory placeholder
 * instead of an iframe that would 404.
 */
export function resolveBackOfficeBase(): string | null {
  const fqdn = DETECTED_MODE.sovereignFQDN
  if (!fqdn) return null
  return `https://marketplace.${fqdn}/back-office`
}

export function BssLayout() {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const activeTab = deriveActiveTab(pathname)
  const backOfficeBase = resolveBackOfficeBase()

  return (
    <div
      className="flex h-full min-h-0 flex-col bg-[var(--color-bg)]"
      data-testid="sov-bss-layout"
    >
      {/* Header — section title + sub-nav tab strip */}
      <header className="border-b border-[var(--color-border)] bg-[var(--color-bg-2)] px-6 pt-5">
        <div className="mb-3 flex items-baseline justify-between">
          <h1
            className="text-xl font-semibold text-[var(--color-text-strong)]"
            data-testid="sov-bss-page-title"
          >
            BSS — Business Support
          </h1>
          <p className="text-xs text-[var(--color-text-dim)]">
            Billing, orders, revenue, vouchers, and tenant administration
          </p>
        </div>
        <nav
          className="flex flex-wrap gap-1"
          data-testid="sov-bss-tab-strip"
          role="tablist"
          aria-label="BSS sections"
        >
          {BSS_TABS.map((tab) => {
            const isActive = activeTab === tab.id
            const cls = isActive
              ? 'border-[var(--color-accent)] text-[var(--color-accent)]'
              : 'border-transparent text-[var(--color-text-dim)] hover:text-[var(--color-text)]'
            return (
              <Link
                key={tab.id}
                to={tab.to as never}
                role="tab"
                aria-selected={isActive}
                aria-current={isActive ? 'page' : undefined}
                className={`-mb-px border-b-2 px-3 py-2 text-sm font-medium no-underline transition-colors ${cls}`}
                data-testid={`sov-bss-tab-${tab.id}`}
              >
                {tab.label}
              </Link>
            )
          })}
        </nav>
      </header>

      {/* Body — the active sub-page renders the iframe via <Outlet/> */}
      <div className="flex flex-1 min-h-0 flex-col">
        {backOfficeBase ? (
          <Outlet />
        ) : (
          <NoMarketplaceHint />
        )}
      </div>
    </div>
  )
}

/**
 * NoMarketplaceHint — rendered when DETECTED_MODE has no Sovereign FQDN
 * (Catalyst-Zero mothership preview). The back-office UI doesn't exist
 * outside a Sovereign cluster, so we surface that instead of iframing
 * a 404.
 */
function NoMarketplaceHint() {
  return (
    <div
      className="m-6 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]"
      data-testid="sov-bss-no-marketplace"
    >
      <p className="font-medium text-[var(--color-text)]">
        BSS is a Sovereign-only surface.
      </p>
      <p className="mt-2">
        Open a deployed Sovereign Console (e.g.&nbsp;
        <span className="font-mono">https://console.&lt;your-sov-fqdn&gt;/bss/billing</span>) to
        manage billing, orders, revenue, vouchers, and tenants.
      </p>
    </div>
  )
}

/**
 * BssIframe — shared iframe wrapper for the per-section pages.
 *
 * Each section page (BillingPage, OrdersPage, …) is a 3-line wrapper
 * that calls this with its `path`. Centralising the iframe attributes
 * keeps sandbox/referrer/title consistent and means we only update the
 * iframe contract in one place.
 *
 * `sandbox` is intentionally permissive — the back-office is FIRST-party
 * content served from a sibling subdomain of the same Sovereign, NOT
 * untrusted third-party UI. We allow scripts + same-origin (so cookie
 * auth works), forms + popups (so order-detail drill-downs can open
 * external receipts), and downloads (so Revenue CSV export works).
 */
export function BssIframe({ path, title }: { path: string; title: string }) {
  const base = resolveBackOfficeBase()
  if (!base) return null
  const src = `${base}/${path}/`
  return (
    <iframe
      key={src}
      src={src}
      title={title}
      className="h-full w-full flex-1 border-0 bg-[var(--color-bg)]"
      sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-downloads allow-modals"
      referrerPolicy="same-origin"
      loading="eager"
      data-testid={`sov-bss-iframe-${path}`}
      data-back-office-src={src}
    />
  )
}
