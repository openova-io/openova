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
import { Allocation } from './pages/Allocation'
import { ChartGallery } from './pages/ChartGallery'
import { Statements } from './pages/Statements'
import { Resources } from './pages/Resources'
import { ResourceDetail } from './pages/ResourceDetail'
import { Anomalies } from './pages/Anomalies'
import { Recommendations } from './pages/Recommendations'

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
            <Route path="/allocation" element={<Allocation />} />
            <Route path="/pricebooks" element={<PriceBooks />} />
            <Route path="/pricebooks/:id" element={<PriceBookEdit />} />
            <Route path="/statements" element={<Statements />} />
            <Route path="/resources" element={<Resources />} />
            <Route path="/resources/:sourceId/:resourceId" element={<ResourceDetail />} />
            <Route path="/anomalies" element={<Anomalies />} />
            <Route path="/recommendations" element={<Recommendations />} />
            {/* Visual regression page for the chart library (#6867); not in the nav. */}
            <Route path="/dev/charts" element={<ChartGallery />} />
          </Route>

          <Route element={<Shell roles={['customer-admin', 'customer-viewer']} />}>
            <Route path="/my/usage" element={<MyUsage />} />
            <Route path="/my/statements" element={<MyStatements />} />
            <Route path="/my/sources" element={<MySources />} />
            <Route path="/my/resources" element={<Resources />} />
            <Route path="/my/resources/:sourceId/:resourceId" element={<ResourceDetail />} />
            <Route path="/my/anomalies" element={<Anomalies />} />
            <Route path="/my/recommendations" element={<Recommendations />} />
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
