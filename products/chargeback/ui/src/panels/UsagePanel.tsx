import { TrendChart, toDailySeries } from '../components/TrendChart'
import { keyOfUsageRow } from '../lib/usageKey'
import { useCallback, useEffect, useState } from 'react'
import { api, asList, errorText } from '../api/client'
import type { InventoryItem, UsageRow } from '../api/types'
import { Empty, Field, Notice } from '../components/ui'
import { monthStart, num, today, when } from '../lib/format'

type GroupBy = 'sku' | 'resource' | 'day'

export function UsagePanel({ customerId }: { customerId: string }) {
  const [from, setFrom] = useState(monthStart())
  const [to, setTo] = useState(today())
  const [groupBy, setGroupBy] = useState<GroupBy>('sku')
  const [rows, setRows] = useState<UsageRow[]>([])
  const [inventory, setInventory] = useState<InventoryItem[] | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    setBusy(true)
    setError('')
    try {
      const q = new URLSearchParams({ from, to, group_by: groupBy })
      const res = await api.get<unknown>(`/customers/${customerId}/usage?${q}`)
      setRows(asList<UsageRow>(res, 'usage'))
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }, [customerId, from, to, groupBy])

  useEffect(() => {
    void load()
  }, [load])

  const loadInventory = async () => {
    try {
      const res = await api.get<unknown>(`/customers/${customerId}/inventory`)
      setInventory(asList<InventoryItem>(res, 'inventory', 'resources'))
    } catch (e) {
      setError(errorText(e))
    }
  }

  // #6866 — the API returns the grouped column as `key` for every grouping.
  // This read r.day / r.resource_id, which the server never sends, so the day
  // and resource views rendered '—' in every row of their first column while
  // the numbers beside them were correct — the table looked populated and
  // unusable at the same time. `key` first, with the old fields as a fallback
  // so a server that does send them still works.
  const keyOf = (r: UsageRow) => keyOfUsageRow(r, groupBy)

  // #6863 — the day view gets a trend above the table. Only for `day`:
  // a bar per SKU or per resource is a ranking, not a trend, and drawing it
  // on a time axis would imply an ordering the data does not have.
  const series = groupBy === 'day' ? toDailySeries(rows) : []

  return (
    <div className="stack">
      {series.length > 1 && (
        <TrendChart points={series} title="Usage per day" format={(v) => String(Math.round(v * 100) / 100)} />
      )}
      <form
        className="inline"
        onSubmit={(e) => {
          e.preventDefault()
          void load()
        }}
      >
        <Field label="From">
          <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
        </Field>
        <Field label="To">
          <input type="date" value={to} onChange={(e) => setTo(e.target.value)} />
        </Field>
        <Field label="Group by">
          <select value={groupBy} onChange={(e) => setGroupBy(e.target.value as GroupBy)}>
            <option value="sku">SKU</option>
            <option value="resource">Resource</option>
            <option value="day">Day</option>
          </select>
        </Field>
        <button className="primary" disabled={busy}>
          Refresh
        </button>
      </form>
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {rows.length === 0 ? (
        <Empty>No usage records in this window.</Empty>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{groupBy === 'sku' ? 'SKU' : groupBy === 'day' ? 'Day' : 'Resource'}</th>
                {groupBy === 'resource' ? <th>Kind</th> : null}
                {groupBy !== 'sku' ? <th>SKU</th> : null}
                <th>Region</th>
                <th className="num">Quantity</th>
                <th>Unit</th>
                <th className="num">Resources</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r, i) => (
                <tr key={i}>
                  <td className="mono">{keyOf(r)}</td>
                  {groupBy === 'resource' ? <td>{r.resource_kind ?? '—'}</td> : null}
                  {groupBy !== 'sku' ? <td className="mono">{r.sku ?? '—'}</td> : null}
                  <td>{r.region ?? '—'}</td>
                  <td className="num">{num(r.quantity)}</td>
                  <td>{r.unit ?? '—'}</td>
                  <td className="num">{r.resource_count ?? '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="row between">
        <h2>Inventory</h2>
        <button onClick={() => void loadInventory()}>{inventory ? 'Reload' : 'Load inventory'}</button>
      </div>
      {inventory === null ? null : inventory.length === 0 ? (
        <Empty>No resources collected yet.</Empty>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Resource</th>
                <th>Kind</th>
                <th>Name</th>
                <th>Region</th>
                <th>First seen</th>
                <th>Last seen</th>
                <th>Deleted</th>
              </tr>
            </thead>
            <tbody>
              {inventory.map((it) => (
                <tr key={`${it.source_id ?? ''}/${it.resource_id}`}>
                  <td className="mono">{it.resource_id}</td>
                  <td>{it.kind}</td>
                  <td>{it.name ?? '—'}</td>
                  <td>{it.region ?? '—'}</td>
                  <td>{when(it.first_seen)}</td>
                  <td>{when(it.last_seen)}</td>
                  <td>{when(it.deleted_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
