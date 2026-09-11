import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { SessionProvider, homeFor, useSession } from './auth/session'
import { Shell } from './layout/Shell'
import { Activate } from './pages/Activate'
import { CustomerDetail } from './pages/CustomerDetail'
import { CustomerImport } from './pages/CustomerImport'
import { CustomerNew } from './pages/CustomerNew'
import { Customers } from './pages/Customers'
import { MyAccount, MyBudgets, MyDiscounts, MyExplore, MyOverview, MyReports, MySources, MyStatements, MyUsage, MyUsers } from './pages/My'
import { Overview } from './pages/Overview'
import { CostExplorer } from './pages/CostExplorer'
import { PriceBookEdit } from './pages/PriceBookEdit'
import { PriceBooks } from './pages/PriceBooks'
import { SignIn } from './pages/SignIn'
import { StatementView } from './pages/StatementView'
import { Allocation } from './pages/Allocation'
import { ChartGallery } from './pages/ChartGallery'
import { Budgets } from './pages/Budgets'
import { Reports } from './pages/Reports'
import { Discounts } from './pages/Discounts'
import { Statements } from './pages/Statements'
import { Resources } from './pages/Resources'
import { ResourceDetail } from './pages/ResourceDetail'
import { Anomalies } from './pages/Anomalies'
import { Recommendations } from './pages/Recommendations'
import { Collections } from './pages/Collections'
import { Billing } from './pages/Billing'
import { Access } from './pages/Access'
import { Capacity } from './pages/Capacity'
import { EstimatePublic } from './pages/EstimatePublic'
import { Leads } from './pages/Leads'
import { Partners } from './pages/Partners'
import { PartnerDetail } from './pages/PartnerDetail'
import { PartnerBill, PartnerHome, PartnerMyAccount, PartnerMyMargin, PartnerMyRetail, PartnerMyUsers } from './pages/Partner'

function Home() {
  const { me, loading } = useSession()
  if (loading) return <div className="single muted">Loading…</div>
  return <Navigate to={homeFor(me)} replace />
}

// Every page is a real path under BrowserRouter at `/` (spec §5) — the Go
// binary serves index.html for any non-/api path, so deep links work. The two
// lenses (DESIGN.md §10.9): any Sovereign-scoped binding opens the Sovereign
// pages; a customer-scoped principal its own. Inside a page, every control is
// rendered by the permission the caller holds (lib/access.ts).
// The console: every signed-in surface, under the session provider.
function ConsoleRoutes() {
  return (
    <SessionProvider>
      <>
        <Routes>
          <Route path="/" element={<Home />} />
          <Route path="/signin" element={<SignIn />} />
          <Route path="/activate/:token" element={<Activate />} />

          <Route element={<Shell lens="sovereign" />}>
            <Route path="/overview" element={<Overview />} />
            <Route path="/explore" element={<CostExplorer />} />
            <Route path="/customers" element={<Customers />} />
            <Route path="/customers/new" element={<CustomerNew />} />
            <Route path="/customers/import" element={<CustomerImport />} />
            <Route path="/customers/:id" element={<CustomerDetail />} />
            <Route path="/leads" element={<Leads />} />
            <Route path="/partners" element={<Partners />} />
            <Route path="/partners/:id" element={<PartnerDetail />} />
            <Route path="/allocation" element={<Allocation />} />
            <Route path="/pricebooks" element={<PriceBooks />} />
            <Route path="/pricebooks/:id" element={<PriceBookEdit />} />
            <Route path="/discounts" element={<Discounts />} />
            <Route path="/budgets" element={<Budgets />} />
            <Route path="/reports" element={<Reports />} />
            <Route path="/statements" element={<Statements />} />
            <Route path="/collections" element={<Collections />} />
            <Route path="/billing" element={<Billing />} />
            <Route path="/access" element={<Access />} />
            <Route path="/resources" element={<Resources />} />
            <Route path="/resources/:sourceId/:resourceId" element={<ResourceDetail />} />
            <Route path="/anomalies" element={<Anomalies />} />
            <Route path="/recommendations" element={<Recommendations />} />
            <Route path="/capacity" element={<Capacity />} />
            {/* Visual regression page for the chart library (#6867); not in the nav. */}
            <Route path="/dev/charts" element={<ChartGallery />} />
          </Route>

          <Route element={<Shell lens="customer" />}>
            <Route path="/my/overview" element={<MyOverview />} />
            <Route path="/my/explore" element={<MyExplore />} />
            <Route path="/my/usage" element={<MyUsage />} />
            <Route path="/my/statements" element={<MyStatements />} />
            <Route path="/my/account" element={<MyAccount />} />
            <Route path="/my/budgets" element={<MyBudgets />} />
            <Route path="/my/reports" element={<MyReports />} />
            <Route path="/my/sources" element={<MySources />} />
            <Route path="/my/users" element={<MyUsers />} />
            <Route path="/my/resources" element={<Resources />} />
            <Route path="/my/resources/:sourceId/:resourceId" element={<ResourceDetail />} />
            <Route path="/my/anomalies" element={<Anomalies />} />
            <Route path="/my/recommendations" element={<Recommendations />} />
            <Route path="/my/discounts" element={<MyDiscounts />} />
          </Route>

          {/* The partner lens (DESIGN.md §11.5): its customers, its own and
              their statements, its account, its margin and its users. */}
          <Route element={<Shell lens="partner" />}>
            <Route path="/partner/overview" element={<PartnerHome />} />
            <Route path="/partner/customers/:id" element={<CustomerDetail />} />
            <Route path="/partner/statements" element={<PartnerBill />} />
            <Route path="/partner/account" element={<PartnerMyAccount />} />
            <Route path="/partner/margin" element={<PartnerMyMargin />} />
            <Route path="/partner/retail" element={<PartnerMyRetail />} />
            <Route path="/partner/users" element={<PartnerMyUsers />} />
          </Route>

          <Route element={<Shell />}>
            <Route path="/statements/:id" element={<StatementView />} />
          </Route>

          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </>
    </SessionProvider>
  )
}

/**
 * The public cost calculator (DESIGN.md §11) is mounted ABOVE the session
 * provider: `/estimate` renders with no console shell, no sign-in and no
 * authenticated call at all — which is what lets the marketplace frame it.
 * Everything else is the console.
 */
export function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/estimate" element={<EstimatePublic />} />
        <Route path="/estimate/:id" element={<EstimatePublic />} />
        <Route path="*" element={<ConsoleRoutes />} />
      </Routes>
    </BrowserRouter>
  )
}
