import { asList } from '../api/client'
import type { CostSource, Customer, CustomerUser, Summary } from '../api/types'
import { useSession } from '../auth/session'
import { Notice, PageHeader, Skeleton } from '../components/ui'
import { can, customerIds } from '../lib/access'
import { lensFor } from '../lib/scope'
import { readKPIs } from '../lib/summary'
import { useQuery } from '../lib/useQuery'
import { AccountPanel } from '../panels/AccountPanel'
import { BudgetsPanel } from '../panels/BudgetsPanel'
import { DiscountsPanel } from '../panels/DiscountsPanel'
import { SourcesPanel } from '../panels/SourcesPanel'
import { StatementsPanel } from '../panels/StatementsPanel'
import { UsersPanel } from '../panels/UsersPanel'
import { ExplorerBody } from './CostExplorer'
import { OverviewBody } from './Overview'
import { ReportsBody } from './Reports'

/**
 * Customer-lens pages (#6867, DESIGN.md §2 "Customer lens"). Every request
 * is scoped server-side to the caller's bindings; the pages only need the id
 * to build paths, and reuse the operator bodies through lensFor(me). What a
 * customer may DO is decided by its permissions on its customer (DESIGN.md
 * §10.9): customer.self.manage for users, sources and PO / tax;
 * account.topup for a checkout.
 */
function useMy() {
  const { me } = useSession()
  const id = me?.customer_id ?? customerIds(me)[0] ?? null
  return {
    me,
    id,
    lens: lensFor(me),
    canManage: can(me, 'customer.self.manage', id),
    canTopup: can(me, 'account.topup', id),
  }
}

function NoCustomer() {
  return (
    <Notice kind="warn">
      This account is not linked to a customer, so there is nothing to show. Ask the operator to add your email under the customer's Users.
    </Notice>
  )
}

/** The currency the customer is billed in, from its cost summary. */
function useMyCurrency(id: string | null): string {
  const sum = useQuery<Summary>(id ? `/customers/${id}/cost/summary` : null)
  return sum.data ? readKPIs(sum.data).currency : ''
}

export function MyOverview() {
  const { id, lens } = useMy()
  if (!id) return <NoCustomer />
  return <OverviewBody lens={lens} title="Overview" />
}

export function MyExplore() {
  const { id, lens } = useMy()
  if (!id) return <NoCustomer />
  return <ExplorerBody lens={lens} />
}

/** /my/usage kept as an alias of the explorer for old links. */
export const MyUsage = MyExplore

export function MyStatements() {
  const { id } = useMy()
  if (!id) return <NoCustomer />
  return (
    <div className="stack">
      <PageHeader title="Statements" sub="Every billing period rated for your account; issued statements are final." />
      <StatementsPanel customerId={id} canIssue={false} />
    </div>
  )
}

/**
 * /my/account — the customer's own ledger. `Top up` here is a checkout: it
 * asks the gateway to collect, and needs account.topup (owner, billing).
 * Recording a transfer, applying credit, credit notes and suspension are the
 * operator's and are not offered.
 */
export function MyAccount() {
  const { id, canTopup } = useMy()
  const cust = useQuery<Customer>(id ? `/customers/${id}` : null)
  const currency = useMyCurrency(id)
  if (!id) return <NoCustomer />
  if (cust.error && !cust.data) {
    return (
      <div className="stack">
        <PageHeader title="Account" />
        <Notice kind="bad">{cust.error}</Notice>
      </div>
    )
  }
  if (!cust.data) return <Skeleton lines={4} />
  return (
    <div className="stack">
      <PageHeader title="Account" sub={canTopup ? 'Your balance, the ledger behind it, and a checkout to top it up.' : 'Your balance and the ledger behind it. An owner or billing user of this customer can top it up.'} />
      <AccountPanel customerId={id} customer={cust.data} currency={currency} canRecord={false} canCheckout={canTopup} canApplyCredit={false} onChanged={cust.reload} />
    </div>
  )
}

export function MyBudgets() {
  const { id } = useMy()
  const currency = useMyCurrency(id)
  if (!id) return <NoCustomer />
  return (
    <div className="stack">
      <PageHeader title="Budgets" sub="Monthly caps set by the operator, with this month's spend and forecast against them." />
      <BudgetsPanel customerId={id} canManage={false} currency={currency} />
    </div>
  )
}

export function MyReports() {
  const { id, lens, canManage } = useMy()
  if (!id) return <NoCustomer />
  return <ReportsBody lens={lens} canManage={canManage} />
}

export function MySources() {
  const { id, canManage } = useMy()
  const src = useQuery<unknown>(id ? `/customers/${id}/sources` : null)
  if (!id) return <NoCustomer />
  const sources = asList<CostSource>(src.data, 'sources')
  return (
    <div className="stack">
      <PageHeader title="Cost sources" sub={canManage ? 'Where your usage is collected from. You may rotate access keys and narrow a source with a scope token; the operator sets regions and projects.' : 'Where your usage is collected from. An owner of this customer may rotate access keys.'} />
      {src.error ? <Notice kind="bad">{src.error}</Notice> : null}
      <SourcesPanel customerId={id} sources={sources} canManage={false} canRotate={canManage} canEditScope={canManage} onChanged={src.reload} loading={src.loading} />
    </div>
  )
}

/** /my/users — the customer's own users (DESIGN.md §10.9); an owner adds and removes. */
export function MyUsers() {
  const { id, canManage } = useMy()
  const cust = useQuery<Customer>(id ? `/customers/${id}` : null)
  const usr = useQuery<unknown>(id ? `/customers/${id}/users` : null)
  if (!id) return <NoCustomer />
  const users = asList<CustomerUser>(usr.data, 'users')
  return (
    <div className="stack">
      <PageHeader title="Users" sub={canManage ? 'Who may sign in for your account, and as what. You may add and remove users.' : 'Who may sign in for your account. An owner may add and remove users.'} />
      {usr.error ? <Notice kind="bad">{usr.error}</Notice> : null}
      <UsersPanel customerId={id} users={users} adminEmail={cust.data?.admin_email ?? ''} canManage={canManage} onChanged={usr.reload} />
    </div>
  )
}

export function MyDiscounts() {
  const { id } = useMy()
  const currency = useMyCurrency(id)
  if (!id) return <NoCustomer />
  return (
    <div className="stack">
      <PageHeader title="Discounts" sub="What is taken off list price on your statements — your own discounts and campaigns that apply to every customer." />
      <DiscountsPanel customerId={id} canManage={false} currency={currency} />
    </div>
  )
}
