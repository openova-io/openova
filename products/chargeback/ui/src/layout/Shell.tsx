import { Navigate, NavLink, Outlet, useNavigate } from 'react-router-dom'
import { useSession } from '../auth/session'
import type { Role } from '../api/types'

const OPERATOR_NAV = [
  ['/overview', 'Overview'],
  ['/customers', 'Customers'],
  ['/pricebooks', 'Price books'],
  ['/statements', 'Statements'],
] as const

const CUSTOMER_NAV = [
  ['/my/usage', 'My usage'],
  ['/my/statements', 'My statements'],
  ['/my/sources', 'My sources'],
] as const

export function Shell({ roles }: { roles?: Role[] }) {
  const { me, loading, logout } = useSession()
  const nav = useNavigate()
  if (loading) return <div className="single muted">Loading…</div>
  if (!me) return <Navigate to="/signin" replace />
  if (roles && !roles.includes(me.role)) return <Navigate to="/" replace />

  const links = me.role === 'operator' ? OPERATOR_NAV : CUSTOMER_NAV
  return (
    <div>
      {me.profile && me.profile !== 'sovereign' ? (
        <div className="banner">profile: {me.profile}</div>
      ) : null}
      <div className="shell">
        <aside className="side">
          <div className="brand">Chargeback</div>
          {links.map(([to, label]) => (
            <NavLink key={to} to={to} className={({ isActive }) => (isActive ? 'active' : '')}>
              {label}
            </NavLink>
          ))}
          <div className="spacer" />
          <div className="who">
            {me.email}
            <br />
            <span className="mono">{me.role}</span>
          </div>
          <button
            onClick={async () => {
              await logout()
              nav('/signin')
            }}
          >
            Sign out
          </button>
        </aside>
        <main className="main">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
