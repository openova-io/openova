import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { SessionProvider, homeFor, useSession } from './auth/session'
import { Shell } from './layout/Shell'
import { Activate } from './pages/Activate'
import { CustomerDetail } from './pages/CustomerDetail'
import { CustomerImport } from './pages/CustomerImport'
import { CustomerNew } from './pages/CustomerNew'
import { Customers } from './pages/Customers'
import { MySources, MyStatements, MyUsage } from './pages/My'
import { Overview } from './pages/Overview'
import { PriceBookEdit } from './pages/PriceBookEdit'
import { PriceBooks } from './pages/PriceBooks'
import { SignIn } from './pages/SignIn'
import { StatementView } from './pages/StatementView'
import { Statements } from './pages/Statements'

function Home() {
  const { me, loading } = useSession()
  if (loading) return <div className="single muted">Loading…</div>
  return <Navigate to={homeFor(me)} replace />
}

// Every page is a real path under BrowserRouter at `/` (spec §5) — the Go
// binary serves index.html for any non-/api path, so deep links work.
export function App() {
  return (
    <SessionProvider>
      <BrowserRouter>
        <Routes>
          <Route path="/" element={<Home />} />
          <Route path="/signin" element={<SignIn />} />
          <Route path="/activate/:token" element={<Activate />} />

          <Route element={<Shell roles={['operator']} />}>
            <Route path="/overview" element={<Overview />} />
            <Route path="/customers" element={<Customers />} />
            <Route path="/customers/new" element={<CustomerNew />} />
            <Route path="/customers/import" element={<CustomerImport />} />
            <Route path="/customers/:id" element={<CustomerDetail />} />
            <Route path="/pricebooks" element={<PriceBooks />} />
            <Route path="/pricebooks/:id" element={<PriceBookEdit />} />
            <Route path="/statements" element={<Statements />} />
          </Route>

          <Route element={<Shell roles={['customer-admin', 'customer-viewer']} />}>
            <Route path="/my/usage" element={<MyUsage />} />
            <Route path="/my/statements" element={<MyStatements />} />
            <Route path="/my/sources" element={<MySources />} />
          </Route>

          <Route element={<Shell />}>
            <Route path="/statements/:id" element={<StatementView />} />
          </Route>

          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </BrowserRouter>
    </SessionProvider>
  )
}
