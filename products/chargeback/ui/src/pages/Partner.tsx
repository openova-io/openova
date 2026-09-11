import { useMemo } from 'react'
import { Link } from 'react-router-dom'
import { asList } from '../api/client'
import type { Partner, Statement } from '../api/types'
import { useSession } from '../auth/session'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Notice, PageHeader, Skeleton } from '../components/ui'
import { primaryPartnerId } from '../lib/access'
import { when } from '../lib/format'
import { formatMoney } from '../lib/money'
import { toNumber } from '../lib/num'
import { billToHelp, billToLabel } from '../lib/partners'
import { statementPeriod, statementStatus } from '../lib/statements'
import { useQuery } from '../lib/useQuery'
import { PartnerAccount, PartnerCustomers, PartnerKPIs, PartnerMargin, PartnerUsers, RetailRuleEditor } from './PartnerDetail'

/**
 * The PARTNER lens (DESIGN.md §11.5) — what a partner owner or viewer sees:
 * its customers, the statements of those customers beside its own wholesale
 * or commission statements, its account, its margin and its users. Nothing
 * of the Sovereign's, and nothing of another partner's.
 */

function usePartner() {
  const { me } = useSession()
  const id = primaryPartnerId(me)
  const q = useQuery<Partner>(id ? `/partners/${id}` : null)
  return { me, id, partner: q.data, error: q.error, reload: q.reload }
}

function NoPartner() {
  return <Notice kind="warn">This account is not linked to a partner, so there is nothing to show. Ask the operator to add your email under the partner's Users.</Notice>
}

/** Analyse — the partner's own customers. */
export function PartnerHome() {
  const { id, partner, error } = usePartner()
  if (!id) return <NoPartner />
  if (error && !partner) return <Notice kind="bad">{error}</Notice>
  if (!partner) return <Skeleton lines={5} />
  return (
    <div className="stack">
      <PageHeader
        title={partner.name}
        sub={
          <>
            {billToLabel(partner.bill_to)} · {billToHelp(partner.bill_to)}
          </>
        }
      />
      <PartnerKPIs partner={partner} />
      <PartnerCustomers partnerId={id} lensBase="/partner/customers" />
    </div>
  )
}

/**
 * Bill — its customers' statements AND its own. The server scopes
 * GET /statements to the partner's customers plus its own party, so this one
 * list is the whole picture: what its customers owe, and what it owes us (or
 * we owe it).
 */
export function PartnerBill() {
  const { id, partner } = usePartner()
  const list = useQuery<unknown>('/statements')
  const rows = useMemo(() => asList<Statement>(list.data, 'statements'), [list.data])
  const partyID = partner?.party_customer_id
  const columns: Column<Statement>[] = [
    {
      key: 'who',
      header: 'Billed to',
      value: (s) => (s.customer_id === partyID ? '' : (s.customer_name ?? '')),
      render: (s) =>
        s.customer_id === partyID ? (
          <>
            {partner?.name ?? 'This partner'}
            <span className="sub">{s.statement_kind === 'commission' ? 'commission we owe you' : 'what you pay us'}</span>
          </>
        ) : (
          <>
            {s.customer_name}
            <span className="sub">your customer</span>
          </>
        ),
    },
    { key: 'period', header: 'Period', value: (s) => s.period_start, render: (s) => <Link to={`/statements/${s.id}`}>{statementPeriod(s)}</Link> },
    { key: 'status', header: 'Status', value: (s) => statementStatus(s), render: (s) => <Badge status={statementStatus(s)} /> },
    { key: 'total', header: 'Total', value: (s) => toNumber(s.total), numeric: true, render: (s) => formatMoney(toNumber(s.total), s.currency) },
    {
      key: 'margin',
      header: 'Your margin',
      value: (s) => toNumber(s.margin_total ?? 0),
      numeric: true,
      render: (s) => (s.margin_total === undefined || s.margin_total === null ? <span className="muted">—</span> : formatMoney(toNumber(s.margin_total), s.currency)),
    },
    { key: 'issued', header: 'Issued', value: (s) => s.issued_at ?? '', render: (s) => (s.issued_at ? when(s.issued_at) : <span className="muted">draft</span>) },
  ]
  if (!id) return <NoPartner />
  return (
    <div className="stack">
      <PageHeader title="Statements" sub="Your customers' statements, and your own wholesale or commission statements." />
      {list.error ? <Notice kind="bad">{list.error}</Notice> : null}
      {list.loading && !list.data ? (
        <Skeleton lines={4} />
      ) : (
        <div className="card pad-0">
          <DataTable
            label="Statements"
            columns={columns}
            rows={rows}
            rowKey={(s) => s.id}
            defaultSort={{ key: 'period', dir: 'desc' }}
            csvName="statements"
            emptyTitle="No statement yet"
            emptyBody="A statement run writes your customers' statements and yours together, once a period has usage."
          />
        </div>
      )}
    </div>
  )
}

/** Account — its own party's ledger. */
export function PartnerMyAccount() {
  const { id, partner, error } = usePartner()
  if (!id) return <NoPartner />
  if (error && !partner) return <Notice kind="bad">{error}</Notice>
  if (!partner) return <Skeleton lines={4} />
  return (
    <div className="stack">
      <PageHeader title="Account" sub="Your balance and the ledger behind it: what you owe us, or what we owe you." />
      <PartnerAccount partner={partner} />
    </div>
  )
}

/** Margin — per customer per service. */
export function PartnerMyMargin() {
  const { id } = usePartner()
  if (!id) return <NoPartner />
  return (
    <div className="stack">
      <PageHeader title="Margin" sub="What your customers pay, what you pay us, and the difference — per customer and per service." />
      <PartnerMargin partnerId={id} />
    </div>
  )
}

/** Retail rule — the partner's own prices. */
export function PartnerMyRetail() {
  const { id, partner, error, reload } = usePartner()
  if (!id) return <NoPartner />
  if (error && !partner) return <Notice kind="bad">{error}</Notice>
  if (!partner) return <Skeleton lines={4} />
  return (
    <div className="stack">
      <PageHeader title="Retail prices" sub="The rule your customers' prices are derived from." />
      <RetailRuleEditor partner={partner} onSaved={reload} />
    </div>
  )
}

/** Users — who signs in as this partner. */
export function PartnerMyUsers() {
  const { id } = usePartner()
  if (!id) return <NoPartner />
  return (
    <div className="stack">
      <PageHeader title="Users" sub="Who signs in as this partner, and as what." />
      <PartnerUsers partnerId={id} />
    </div>
  )
}
