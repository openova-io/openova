import { Navigate, NavLink, Outlet, useNavigate } from 'react-router-dom'
import type { Me, Permission } from '../api/types'
import { useSession } from '../auth/session'
import { can, customerIds, displayRole, isSovereign, roleLabel } from '../lib/access'

// Sovereign-admin lens: Analyse · Plan · Bill · Configure (DESIGN.md §2, §11).
// Every item may name the permission it needs (DESIGN.md §10.9); items
// without one are readable by any principal on the lens, and the server
// still filters every row by the session's scope.
type NavItem = readonly [to: string, label: string, icon: string, needs?: Permission]
type NavGroup = readonly [title: string, items: readonly NavItem[]]

const SOVEREIGN_NAV: readonly NavGroup[] = [
  [
    'Analyse',
    [
      ['/overview', 'Overview', '◐'],
      ['/explore', 'Cost explorer', '▤'],
      ['/resources', 'Resources', '▦'],
      ['/anomalies', 'Anomalies', '△'],
      ['/recommendations', 'Recommendations', '✓'],
    ],
  ],
  // Plan (DESIGN.md §11): the operator's picture of the cloud underneath.
  // Readable by any Sovereign principal (metering.read at the Sovereign);
  // totals, footprints and caps are edited with capacity.manage inside.
  ['Plan', [['/capacity', 'Capacity', '▥']]],
  [
    'Bill',
    [
      ['/statements', 'Statements', '≡'],
      ['/collections', 'Collections', '⧗'],
      ['/budgets', 'Budgets', '◔'],
      ['/reports', 'Reports', '✉'],
    ],
  ],
  [
    'Configure',
    [
      ['/customers', 'Customers', '⌂'],
      ['/leads', 'Leads', '✦', 'customers.manage'],
      ['/pricebooks', 'Price books', '¤'],
      ['/discounts', 'Discounts', '%'],
      ['/allocation', 'Allocation', '⇶'],
      ['/billing', 'Billing', '¶'],
      ['/access', 'Access', '⚿', 'settings.manage'],
    ],
  ],
] as const

// The customer lens (DESIGN.md §10.9). A customer never sees the Sovereign's
// Bill / Configure groups; its own are its statements, its account and its
// users — each gated by the permission the customer role holds.
const CUSTOMER_NAV: readonly NavGroup[] = [
  [
    'Analyse',
    [
      ['/my/overview', 'Overview', '◐'],
      ['/my/explore', 'Cost explorer', '▤'],
      ['/my/resources', 'Resources', '▦'],
    ],
  ],
  [
    'Bill',
    [
      ['/my/statements', 'Statements', '≡'],
      ['/my/account', 'Account', '◎'],
      ['/my/budgets', 'Budgets', '◔'],
      ['/my/reports', 'Reports', '✉'],
    ],
  ],
  [
    'Configure',
    [
      ['/my/sources', 'Cost sources', '⇄'],
      ['/my/discounts', 'Discounts', '%'],
      ['/my/users', 'Users', '☺', 'customer.self.manage'],
    ],
  ],
] as const

export type Lens = 'sovereign' | 'customer'

/** The groups a principal sees, with the items it lacks permission for removed. */
export function navFor(me: Me): readonly NavGroup[] {
  const sovereign = isSovereign(me)
  const customerId = sovereign ? null : (customerIds(me)[0] ?? null)
  const groups = sovereign ? SOVEREIGN_NAV : CUSTOMER_NAV
  return groups
    .map(([title, items]) => [title, items.filter(([, , , needs]) => !needs || can(me, needs, customerId))] as const)
    .filter(([, items]) => items.length > 0)
}

export function Shell({ lens }: { lens?: Lens }) {
  const { me, loading, logout } = useSession()
  const nav = useNavigate()
  if (loading) return <div className="single muted">Loading…</div>
  if (!me) return <Navigate to="/signin" replace />
  const mine: Lens = isSovereign(me) ? 'sovereign' : 'customer'
  if (lens && lens !== mine) return <Navigate to="/" replace />

  const groups = navFor(me)
  const role = displayRole(me)
  return (
    <div className="shell">
      <aside className="side">
        <div className="brand">
          <span className="dot" />
          Chargeback
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
          Sign out
        </button>
      </aside>
      <main className="main">
        {me.profile && me.profile !== 'sovereign' ? <div className="banner">profile: {me.profile}</div> : null}
        <div className="main-inner">
          <Outlet />
        </div>
      </main>
    </div>
  )
}
