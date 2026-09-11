import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { asList } from '../api/client'
import type { Contract, ContractItem, Customer } from '../api/types'
import { Badge, EmptyState, Notice, Skeleton } from '../components/ui'
import { contractItemText, daysToEnd, renewalDue, termText } from '../lib/contracts'
import { today } from '../lib/format'
import { formatMoney } from '../lib/money'
import { toNumber } from '../lib/num'
import { useQuery } from '../lib/useQuery'
import { NewContractModal } from '../pages/Contracts'

/**
 * The Contract tab of the customer page (DESIGN.md §15.9): the agreement
 * where the customer is, rather than only in a directory. A customer's own
 * principal reads it too — the server scopes the list — which is how a
 * customer sees the terms it signed without an operator sending them over.
 */
export function ContractPanel({ customer, canManage }: { customer: Customer; canManage: boolean }) {
  const q = useQuery<unknown>(`/customers/${customer.id}/contracts`)
  const [creating, setCreating] = useState(false)
  const contracts = useMemo(() => asList<Contract>(q.data, 'contracts'), [q.data])
  const on = today()

  return (
    <div className="stack">
      {q.error ? <Notice kind="bad">{q.error}</Notice> : null}
      <div className="card">
        <div className="card-head">
          <h2>Contracts</h2>
          {canManage ? (
            <button className="primary small" onClick={() => setCreating(true)}>
              New contract
            </button>
          ) : null}
        </div>
        {q.loading && !contracts.length ? (
          <Skeleton lines={3} />
        ) : contracts.length === 0 ? (
          <EmptyState title="No contract">
            {customer.name} is rated at its price books alone: no committed use, no contract allowance and no monthly minimum. A contract adds the term, those lines and the minimum whose shortfall is
            invoiced as a true-up.
          </EmptyState>
        ) : (
          <div className="stack tight">
            {contracts.map((c) => {
              const items = asList<ContractItem>(c.items ?? [], 'items')
              const days = daysToEnd(c, on)
              return (
                <div key={c.id} className="card flat stack tight">
                  <div className="row between">
                    <div>
                      <Link to={`/contracts/${c.id}`}>
                        <b>{c.name}</b>
                      </Link>{' '}
                      <Badge status={c.status} kind={c.status === 'active' ? 'ok' : c.status === 'draft' ? 'warn' : 'bad'} />
                    </div>
                    <span className="muted small nowrap">{termText(c)}</span>
                  </div>
                  <div className="muted small">
                    {toNumber(c.minimum_commitment) > 0 ? (
                      <>Monthly minimum {formatMoney(toNumber(c.minimum_commitment), c.currency)} — a period below it carries a true-up line. </>
                    ) : (
                      <>No monthly minimum. </>
                    )}
                    {c.auto_renew ? <>Renews for another {c.term_months} months on {c.renewal_date}.</> : <>Ends {c.ends_on} unless it is renewed.</>}
                    {days !== null && c.status === 'active' ? <> {days >= 0 ? `${days} day${days === 1 ? '' : 's'} to go.` : `${-days} day${days === -1 ? '' : 's'} past the end date.`}</> : null}
                  </div>
                  {renewalDue(c, on) ? <Notice kind="warn">Inside the renewal notice window — {c.auto_renew ? 'it renews automatically unless it is changed' : 'it expires unless it is renewed'}.</Notice> : null}
                  {items.length ? (
                    <ul style={{ margin: 0, paddingLeft: 18 }}>
                      {items.map((it, i) => (
                        <li key={it.id ?? `${it.kind}-${it.sku}-${i}`} className="small">
                          {contractItemText(it, c.currency)}
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <span className="muted small">No committed-use or allowance lines.</span>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </div>
      {creating ? (
        <NewContractModal
          customers={[customer]}
          customerId={customer.id}
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false)
            void q.reload()
          }}
        />
      ) : null}
    </div>
  )
}
