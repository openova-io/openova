import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { api, asList, errorText } from '../api/client'
import type { Customer, RunResult, Statement } from '../api/types'
import { Field, Notice } from '../components/ui'
import { lastMonth } from '../lib/format'
import { StatementTable } from '../panels/StatementsPanel'

/** Run a period + list statements across customers (spec §5, statements). */
export function Statements() {
  const [customers, setCustomers] = useState<Customer[]>([])
  const [customerId, setCustomerId] = useState('')
  const [period, setPeriod] = useState(lastMonth())
  const [rows, setRows] = useState<Statement[]>([])
  const [error, setError] = useState('')
  const [run, setRun] = useState<RunResult | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    api
      .get<unknown>('/customers')
      .then((r) => setCustomers(asList<Customer>(r, 'customers')))
      .catch((e) => setError(errorText(e)))
  }, [])

  const load = useCallback(async () => {
    const targets = customerId ? customers.filter((c) => c.id === customerId) : customers
    if (targets.length === 0) {
      setRows([])
      return
    }
    try {
      const lists = await Promise.all(
        targets.map(async (c) =>
          asList<Statement>(await api.get<unknown>(`/customers/${c.id}/statements`), 'statements').map((s) => ({
            ...s,
            customer_name: s.customer_name ?? c.name,
          })),
        ),
      )
      setRows(lists.flat().sort((a, b) => (a.period_start < b.period_start ? 1 : -1)))
      setError('')
    } catch (e) {
      setError(errorText(e))
    }
  }, [customerId, customers])

  useEffect(() => {
    void load()
  }, [load])

  const runPeriod = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const body: Record<string, string> = { period }
      if (customerId) body.customer_id = customerId
      setRun(await api.post<RunResult>('/statements/run', body))
      await load()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="stack">
      <h1>Statements</h1>
      <form className="card inline" onSubmit={runPeriod}>
        <Field label="Period">
          <input type="month" value={period} onChange={(e) => setPeriod(e.target.value)} required />
        </Field>
        <Field label="Customer">
          <select value={customerId} onChange={(e) => setCustomerId(e.target.value)}>
            <option value="">all customers</option>
            {customers.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
        </Field>
        <button className="primary" disabled={busy}>
          Run period
        </button>
      </form>
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {run ? (
        <Notice kind="ok">
          Period {run.period ?? period} rated
          {typeof run.created === 'number' ? ` — ${run.created} draft statement(s)` : ''}
          {Array.isArray(run.statements) ? ` — ${run.statements.length} draft statement(s)` : ''}.
        </Notice>
      ) : null}
      <StatementTable rows={rows} canIssue onChanged={load} showCustomer />
    </div>
  )
}
