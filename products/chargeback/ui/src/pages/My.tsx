import { asList } from '../api/client'
import type { CostSource, Summary } from '../api/types'
import { useSession } from '../auth/session'
import { Notice, PageHeader } from '../components/ui'
import { lensFor } from '../lib/scope'
import { readKPIs } from '../lib/summary'
import { useQuery } from '../lib/useQuery'
import { BudgetsPanel } from '../panels/BudgetsPanel'
import { DiscountsPanel } from '../panels/DiscountsPanel'
import { SourcesPanel } from '../panels/SourcesPanel'
import { StatementsPanel } from '../panels/StatementsPanel'
import { ExplorerBody } from './CostExplorer'
import { OverviewBody } from './Overview'

/**
 * Customer-lens pages (#6867, DESIGN.md §2 "Customer lens"). Every request
 * is scoped server-side to session.customer_id; the pages only need the id
 * to build paths, and reuse the operator bodies through lensFor(me).
 */
function useMy() {
  const { me } = useSession()
  return { me, id: me?.customer_id ?? null, lens: lensFor(me), isAdmin: me?.role === 'customer-admin' }
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

export function MySources() {
  const { id, isAdmin } = useMy()
  const src = useQuery<unknown>(id ? `/customers/${id}/sources` : null)
  if (!id) return <NoCustomer />
  const sources = asList<CostSource>(src.data, 'sources')
  return (
    <div className="stack">
      <PageHeader title="Cost sources" sub={isAdmin ? 'Where your usage is collected from. You may rotate access keys and narrow a source with a scope token; the operator sets regions and projects.' : 'Where your usage is collected from. A customer-admin may rotate access keys.'} />
      {src.error ? <Notice kind="bad">{src.error}</Notice> : null}
      <SourcesPanel customerId={id} sources={sources} canManage={false} canRotate={isAdmin} canEditScope={isAdmin} onChanged={src.reload} loading={src.loading} />
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
