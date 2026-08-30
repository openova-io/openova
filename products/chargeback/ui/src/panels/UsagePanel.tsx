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

  const keyOf = (r: UsageRow) => (groupBy === 'sku' ? r.sku : groupBy === 'day' ? r.day : r.resource_id) ?? '—'

  return (
    <div className="stack">
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
