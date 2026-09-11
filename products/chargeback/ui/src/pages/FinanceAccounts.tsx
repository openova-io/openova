import { useEffect, useState } from 'react'
import { api } from '../api/client'
import type { AccountMapping, AccountMappings } from '../api/types'
import { useSession } from '../auth/session'
import { Empty, Field, Notice, PageHeader, Skeleton } from '../components/ui'
import { can } from '../lib/access'
import { splitMappings, unmappedKeys } from '../lib/finance'
import { when } from '../lib/format'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/**
 * Finance → Account mapping (DESIGN.md §18.2): the key this product books to
 * against the account code the operator's finance system posts to.
 *
 * The keys are fixed and the codes are the operator's. A key with no code
 * posts nothing, so the journal cannot balance and the export is refused —
 * which the page says, rather than leaving it to be discovered at year end.
 */
const KEY_HELP: Readonly<Record<string, string>> = {
  receivable: 'what customers owe — an issued invoice debits it, a payment credits it',
  cash: 'money that moved, for a transfer or an internal recharge',
  gateway_clearing: 'money a gateway holds between collecting and settling',
  customer_advances: 'money held on account: a top-up, and a payment beyond what it settled',
  tax_payable: 'tax an invoice owes, one line per rule the invoice froze',
  revenue: 'the catch-all revenue account, for a service with no account of its own',
  discounts: 'what discounts took off the list price, as a contra-revenue line',
  credit_notes: 'the reduction of an invoice through a credit note',
  write_offs: 'a receivable given up on',
  gateway_fees: 'what a gateway kept, as reconciliation discovered it',
  commission: 'what a partner is owed on a commission statement',
}

export function FinanceAccounts() {
  const q = useQuery<AccountMappings>('/finance/accounts')
  const act = useAction()
  const { me } = useSession()
  const mayEdit = can(me, 'settings.manage')
  const [draft, setDraft] = useState<Record<string, string>>({})
  const [newKey, setNewKey] = useState('')
  const [newCode, setNewCode] = useState('')

  useEffect(() => {
    setDraft({})
  }, [q.data])

  const codeOf = (m: AccountMapping) => draft[m.key] ?? m.account_code ?? ''
  const dirty = Object.keys(draft).some((k) => draft[k] !== (q.data?.mappings.find((m) => m.key === k)?.account_code ?? ''))

  const save = async () => {
    const mappings = Object.entries(draft).map(([key, account_code]) => ({
      key,
      account_code,
      description: q.data?.mappings.find((m) => m.key === key)?.description ?? '',
    }))
    if (mappings.length === 0) return
    await act.run(`${mappings.length} account${mappings.length === 1 ? '' : 's'} saved`, () => api.put('/finance/accounts', { mappings }), q.reload)
  }

  const addService = async () => {
    const key = newKey.trim().toLowerCase()
    const prefix = q.data?.revenue_key_prefix ?? 'revenue.'
    const full = key.startsWith(prefix) ? key : prefix + key
    const done = await act.run(`${full} mapped`, () => api.put('/finance/accounts', { mappings: [{ key: full, account_code: newCode.trim(), description: `Revenue, ${key.replace(prefix, '')}` }] }), q.reload)
    if (done) {
      setNewKey('')
      setNewCode('')
    }
  }

  const { fixed, services } = splitMappings(q.data?.mappings, q.data?.keys)
  const missing = unmappedKeys(q.data?.mappings)

  const row = (m: AccountMapping) => (
    <tr key={m.key}>
      <td>
        <span className="mono">{m.key}</span>
        <span className="sub">{KEY_HELP[m.key] ?? m.description ?? ''}</span>
      </td>
      <td>
        {mayEdit ? (
          <input className="mono" value={codeOf(m)} aria-label={`Account code for ${m.key}`} placeholder="unmapped" onChange={(e) => setDraft({ ...draft, [m.key]: e.target.value })} />
        ) : (
          <span className="mono">{m.account_code || <span className="muted">unmapped</span>}</span>
        )}
      </td>
      <td>{m.description || <span className="muted">—</span>}</td>
      <td className="muted">{m.updated_at ? <>{when(m.updated_at)}{m.updated_by ? <span className="sub">{m.updated_by}</span> : null}</> : '—'}</td>
    </tr>
  )

  return (
    <div className="stack">
      <PageHeader
        title="Account mapping"
        sub="the key this product books to, against the code your finance system posts to"
        actions={
          mayEdit ? (
            <button className="primary" disabled={!dirty || act.busy} onClick={() => void save()}>
              Save
            </button>
          ) : undefined
        }
      />

      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {q.error ? <Notice kind="bad">{q.error}</Notice> : null}
      {missing.length ? <Notice kind="warn">No code is mapped for {missing.join(', ')}. A key with no code posts nothing, so the journal will not balance and the export is refused.</Notice> : null}

      {q.loading && !q.data ? (
        <Skeleton lines={8} />
      ) : (
        <>
          <section className="card">
            <h2>Accounts</h2>
            <table className="table" aria-label="Account mapping">
              <thead>
                <tr>
                  <th>Key</th>
                  <th>Account code</th>
                  <th>Name</th>
                  <th>Last changed</th>
                </tr>
              </thead>
              <tbody>{fixed.map(row)}</tbody>
            </table>
          </section>

          <section className="card">
            <h2>Revenue by service</h2>
            <p className="muted">A service with an account of its own is posted to it; everything else falls back to the catch-all revenue account.</p>
            {services.length ? (
              <table className="table" aria-label="Revenue accounts by service">
                <thead>
                  <tr>
                    <th>Key</th>
                    <th>Account code</th>
                    <th>Name</th>
                    <th>Last changed</th>
                  </tr>
                </thead>
                <tbody>{services.map(row)}</tbody>
              </table>
            ) : (
              <Empty>No service has an account of its own yet; all revenue posts to the catch-all account.</Empty>
            )}
            {mayEdit ? (
              <div className="row">
                <Field label="Service" help={`the SKU's first segment — ecs, evs, k8s, plan`}>
                  <input value={newKey} onChange={(e) => setNewKey(e.target.value)} placeholder="ecs" />
                </Field>
                <Field label="Account code">
                  <input value={newCode} onChange={(e) => setNewCode(e.target.value)} placeholder="4010" />
                </Field>
                <button disabled={!newKey.trim() || !newCode.trim() || act.busy} onClick={() => void addService()}>
                  Add
                </button>
              </div>
            ) : null}
          </section>
        </>
      )}
    </div>
  )
}
