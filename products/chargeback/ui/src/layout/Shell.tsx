import { Navigate, NavLink, Outlet, useNavigate } from 'react-router-dom'
import type { Me, Permission } from '../api/types'
import { useSession } from '../auth/session'
import { t, type Key } from '../i18n'
import { can, canPartner, customerIds, displayRole, isPartner, isSovereign, primaryPartnerId, roleLabel } from '../lib/access'

// Sovereign-admin lens: Analyse · Plan · Bill · Configure (DESIGN.md §2, §11).
// Every item may name the permission it needs (DESIGN.md §10.9); items
// without one are readable by any principal on the lens, and the server
// still filters every row by the session's scope.
//
// A nav item carries a catalogue KEY, not a word: navFor resolves it at the
// moment it is read, so the sidebar follows the active locale rather than
// whichever one happened to be set when this module was first imported. The
// `nav.` prefix is what keeps `t(label)` type-safe — every key under it is a
// plain string with no values to interpolate.
type NavKey = Extract<Key, `nav.${string}`>
type NavItemSpec = readonly [to: string, label: NavKey, icon: string, needs?: Permission]
type NavGroupSpec = readonly [title: NavKey, items: readonly NavItemSpec[]]

/** One resolved item: the label is the word the sidebar shows. */
export type NavItem = readonly [to: string, label: string, icon: string, needs?: Permission]
export type NavGroup = readonly [title: string, items: readonly NavItem[]]

const SOVEREIGN_NAV: readonly NavGroupSpec[] = [
  [
    'nav.group.analyse',
    [
      ['/overview', 'nav.overview', '◐'],
      ['/explore', 'nav.explore', '▤'],
      ['/resources', 'nav.resources', '▦'],
      ['/anomalies', 'nav.anomalies', '△'],
      ['/recommendations', 'nav.recommendations', '✓'],
    ],
  ],
  // Plan (DESIGN.md §11): the operator's picture of the cloud underneath.
  // Readable by any Sovereign principal (metering.read at the Sovereign);
  // totals, footprints and caps are edited with capacity.manage inside.
  ['nav.group.plan', [['/capacity', 'nav.capacity', '▥']]],
  [
    'nav.group.bill',
    [
      ['/statements', 'nav.statements', '≡'],
      ['/collections', 'nav.collections', '⧗'],
      ['/budgets', 'nav.budgets', '◔'],
      ['/reports', 'nav.reports', '✉'],
    ],
  ],
  // Finance (DESIGN.md §18) — the handover an operator's finance department
  // posts from: the journal, the settlement reconciliation, the period close
  // and the account map. Reading needs audit.read (with metering.read, which
  // every Sovereign role carries); closing, reopening and editing the map
  // need settings.manage, and the pages render those controls accordingly.
  [
    'nav.group.finance',
    [
      ['/finance/journal', 'nav.journal', '⎘', 'audit.read'],
      ['/finance/reconciliation', 'nav.reconciliation', '⇄', 'audit.read'],
      ['/finance/periods', 'nav.periods', '⊟', 'audit.read'],
      ['/finance/accounts', 'nav.accountMapping', '#', 'audit.read'],
    ],
  ],
  [
    'nav.group.configure',
    [
      ['/customers', 'nav.customers', '⌂'],
      ['/leads', 'nav.leads', '✦', 'customers.manage'],
      ['/partners', 'nav.partners', '⇋'],
      ['/contracts', 'nav.contracts', '§'],
      ['/pricebooks', 'nav.pricebooks', '¤'],
      ['/discounts', 'nav.discounts', '%'],
      ['/allocation', 'nav.allocation', '⇶'],
      ['/billing', 'nav.billing', '¶'],
      ['/tax', 'nav.tax', '⚖', 'metering.read'],
      // Notification management (DESIGN.md §21). Reading the catalogue is
      // metering.read — what the product sends is not a secret and every
      // Sovereign role may see it; the Sovereign-wide switches on the page
      // are rendered by settings.manage inside.
      ['/notifications', 'nav.notifications', '✉', 'metering.read'],
      ['/access', 'nav.access', '⚿', 'settings.manage'],
    ],
  ],
] as const

// The customer lens (DESIGN.md §10.9). A customer never sees the Sovereign's
// Bill / Configure groups; its own are its statements, its account and its
// users — each gated by the permission the customer role holds.
const CUSTOMER_NAV: readonly NavGroupSpec[] = [
  [
    'nav.group.analyse',
    [
      ['/my/overview', 'nav.overview', '◐'],
      ['/my/explore', 'nav.explore', '▤'],
      ['/my/resources', 'nav.resources', '▦'],
      // DESIGN.md §19 — the customer's own spend by cost centre. Reading is
      // metering.read, which every customer role carries; the edits on the
      // page are customers.manage and are simply not rendered here.
      ['/my/cost-centres', 'nav.costCentres', '⊞'],
    ],
  ],
  [
    'nav.group.bill',
    [
      ['/my/statements', 'nav.statements', '≡'],
      ['/my/account', 'nav.account', '◎'],
      // DESIGN.md §16 — the card kept on file. Adding or removing one is
      // account.topup, the same permission a top-up needs; a viewer sees
      // the page and is offered nothing on it.
      ['/my/payment-methods', 'nav.paymentMethods', '▭'],
      ['/my/budgets', 'nav.budgets', '◔'],
      ['/my/reports', 'nav.reports', '✉'],
    ],
  ],
  [
    'nav.group.configure',
    [
      ['/my/sources', 'nav.sources', '⇄'],
      ['/my/discounts', 'nav.discounts', '%'],
      ['/my/users', 'nav.users', '☺', 'customer.self.manage'],
      // DESIGN.md §21 — which messages this account receives, and what
      // happened to each one. Readable by any customer role (metering.read);
      // the switches are rendered by customer.self.manage inside, and an
      // invoice or a dunning notice offers no switch at all.
      ['/my/notifications', 'nav.notifications', '✉'],
    ],
  ],
] as const

// The PARTNER lens (DESIGN.md §13.5): a principal bound to a partner reads
// its own customers, THEIR COST ANALYSIS, its own and their statements, its
// account, its margin and its users — and none of the Sovereign's pages.
//
// Analyse is the same cost explorer, resource list, anomaly detector and
// recommendation set the operator has; the server confines every one of them
// to the customers assigned to the partner, so a reseller answers "what is
// each of my customers costing" without a page of its own.
const PARTNER_NAV: readonly NavGroupSpec[] = [
  [
    'nav.group.analyse',
    [
      ['/partner/overview', 'nav.myCustomers', '⌂'],
      ['/partner/explore', 'nav.explore', '▤'],
      ['/partner/resources', 'nav.resources', '▦'],
      ['/partner/anomalies', 'nav.anomalies', '△'],
      ['/partner/recommendations', 'nav.recommendations', '✓'],
    ],
  ],
  [
    'nav.group.bill',
    [
      ['/partner/statements', 'nav.statements', '≡'],
      ['/partner/account', 'nav.account', '◎'],
      ['/partner/margin', 'nav.margin', '⧉'],
    ],
  ],
  [
    'nav.group.configure',
    [
      ['/partner/retail', 'nav.retail', '¤', 'partner.self.manage'],
      ['/partner/users', 'nav.users', '☺', 'partner.self.manage'],
    ],
  ],
] as const

export type Lens = 'sovereign' | 'partner' | 'customer'

/** One group with its keys resolved against the active locale. */
function resolve([title, items]: readonly [NavKey, readonly NavItemSpec[]]): NavGroup {
  // An item without a permission keeps a THREE-element tuple, exactly as the
  // inline table produced: the arity is part of the shape callers read.
  const resolved = items.map(([to, label, icon, needs]) => (needs === undefined ? ([to, t(label), icon] as const) : ([to, t(label), icon, needs] as const)))
  return [t(title), resolved] as const
}

/** The groups a principal sees, with the items it lacks permission for removed. */
export function navFor(me: Me): readonly NavGroup[] {
  const sovereign = isSovereign(me)
  const partner = isPartner(me)
  if (partner) {
    const partnerId = primaryPartnerId(me)
    return PARTNER_NAV.map(([title, items]) => [title, items.filter(([, , , needs]) => !needs || canPartner(me, needs, partnerId))] as const)
      .filter(([, items]) => items.length > 0)
      .map(resolve)
  }
  const customerId = sovereign ? null : (customerIds(me)[0] ?? null)
  const groups = sovereign ? SOVEREIGN_NAV : CUSTOMER_NAV
  return groups
    .map(([title, items]) => [title, items.filter(([, , , needs]) => !needs || can(me, needs, customerId))] as const)
    .filter(([, items]) => items.length > 0)
    .map(resolve)
}

export function Shell({ lens }: { lens?: Lens }) {
  const { me, loading, logout } = useSession()
  const nav = useNavigate()
  if (loading) return <div className="single muted">{t('shell.loading')}</div>
  if (!me) return <Navigate to="/signin" replace />
  const mine: Lens = isSovereign(me) ? 'sovereign' : isPartner(me) ? 'partner' : 'customer'
  if (lens && lens !== mine) return <Navigate to="/" replace />

  const groups = navFor(me)
  const role = displayRole(me)
  return (
    <div className="shell">
      <aside className="side">
        <div className="brand">
          <span className="dot" />
          {t('common.product')}
          {me.profile && me.profile !== 'sovereign' ? <span className="env">{me.profile}</span> : null}
        </div>
        {groups.map(([title, items]) => (
          <div key={title}>
            <div className="group">{title}</div>
            {items.map(([to, label, icon]) => (
              <NavLink key={to} to={to} className={({ isActive }) => (isActive ? 'active' : '')}>
                <span className="ico" aria-hidden>
                  {icon}
                </span>
                {label}
              </NavLink>
            ))}
          </div>
        ))}
        <div className="spacer" />
        <div className="who">
          {me.email}
          <br />
          <span className="role" title={role}>
            {roleLabel(role)}
          </span>
        </div>
        <button
          className="ghost"
          onClick={async () => {
            await logout()
            nav('/signin')
          }}
        >
          {t('shell.signOut')}
        </button>
      </aside>
      <main className="main">
        {me.profile && me.profile !== 'sovereign' ? <div className="banner">{t('shell.profileBanner', { profile: me.profile })}</div> : null}
        <div className="main-inner">
          <Outlet />
        </div>
      </main>
    </div>
  )
}
