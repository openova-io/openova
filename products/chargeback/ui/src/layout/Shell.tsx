import { Navigate, NavLink, Outlet, useNavigate } from 'react-router-dom'
import { useSession } from '../auth/session'
import type { Role } from '../api/types'

// Sovereign-admin lens: Analyse · Bill · Configure (DESIGN.md §2).
type NavItem = readonly [to: string, label: string, icon: string]
type NavGroup = readonly [title: string, items: readonly NavItem[]]

const OPERATOR_NAV: readonly NavGroup[] = [
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
  [
    'Bill',
    [
      ['/statements', 'Statements', '≡'],
      ['/budgets', 'Budgets', '◔'],
    ],
  ],
  [
    'Configure',
    [
      ['/customers', 'Customers', '⌂'],
      ['/pricebooks', 'Price books', '¤'],
      ['/discounts', 'Discounts', '%'],
      ['/allocation', 'Allocation', '⇶'],
    ],
  ],
] as const

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
      ['/my/budgets', 'Budgets', '◔'],
    ],
  ],
  [
    'Configure',
    [
      ['/my/sources', 'Cost sources', '⇄'],
      ['/my/discounts', 'Discounts', '%'],
    ],
  ],
] as const

export function Shell({ roles }: { roles?: Role[] }) {
  const { me, loading, logout } = useSession()
  const nav = useNavigate()
  if (loading) return <div className="single muted">Loading…</div>
  if (!me) return <Navigate to="/signin" replace />
  if (roles && !roles.includes(me.role)) return <Navigate to="/" replace />

  const groups = me.role === 'operator' ? OPERATOR_NAV : CUSTOMER_NAV
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
          <span className="role">{me.role}</span>
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
