import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, asList, errorText } from '../api/client'
import type { PriceBook } from '../api/types'
import { Empty, Field, Notice } from '../components/ui'
import { day } from '../lib/format'

export function PriceBooks() {
  const nav = useNavigate()
  const [rows, setRows] = useState<PriceBook[]>([])
  const [error, setError] = useState('')
  const [showNew, setShowNew] = useState(false)
  const [name, setName] = useState('')
  const [currency, setCurrency] = useState('OMR')
  const [divisor, setDivisor] = useState('8760')
  const [billStopped, setBillStopped] = useState('compute')
  const [effectiveFrom, setEffectiveFrom] = useState('')

  const load = useCallback(async () => {
    try {
      setRows(asList<PriceBook>(await api.get<unknown>('/pricebooks'), 'pricebooks', 'price_books'))
      setError('')
    } catch (e) {
      setError(errorText(e))
    }
  }, [])
  useEffect(() => {
    void load()
  }, [load])

  const create = async (e: FormEvent) => {
    e.preventDefault()
    try {
      const b = await api.post<PriceBook>('/pricebooks', {
        name: name.trim(),
        currency: currency.trim().toUpperCase(),
        annual_divisor: Number(divisor),
        bill_stopped: billStopped,
        effective_from: effectiveFrom || null,
      })
      nav(`/pricebooks/${b.id}`)
    } catch (err) {
      setError(errorText(err))
    }
  }

  return (
    <div className="stack">
      <div className="row between">
        <h1>Price books</h1>
        <button className="primary" onClick={() => setShowNew((v) => !v)}>
          New price book
        </button>
      </div>
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {showNew ? (
        <form className="card" onSubmit={create}>
          <div className="grid2">
            <Field label="Name">
              <input value={name} onChange={(e) => setName(e.target.value)} required />
            </Field>
            <Field label="Currency">
              <input value={currency} onChange={(e) => setCurrency(e.target.value)} maxLength={3} required />
            </Field>
            <Field label="Annual divisor (hours per year)">
              <input type="number" min={1} value={divisor} onChange={(e) => setDivisor(e.target.value)} required />
            </Field>
            <Field label="Stopped compute is billed as">
              <select value={billStopped} onChange={(e) => setBillStopped(e.target.value)}>
                <option value="compute">compute (full)</option>
                <option value="storage-only">storage-only</option>
                <option value="none">none</option>
              </select>
            </Field>
            <Field label="Effective from">
              <input type="date" value={effectiveFrom} onChange={(e) => setEffectiveFrom(e.target.value)} />
            </Field>
          </div>
          <button className="primary">Create</button>
        </form>
      ) : null}
      {rows.length === 0 ? (
        <Empty>No price books. Create one, then import the SKU list as CSV.</Empty>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Currency</th>
              <th className="num">Annual divisor</th>
              <th>Stopped compute</th>
              <th>Effective from</th>
              <th className="num">Items</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((b) => (
              <tr key={b.id}>
                <td>
                  <Link to={`/pricebooks/${b.id}`}>{b.name}</Link>
                </td>
                <td>{b.currency}</td>
                <td className="num">{b.annual_divisor}</td>
                <td>{b.bill_stopped}</td>
                <td>{day(b.effective_from)}</td>
                <td className="num">{Array.isArray(b.items) ? b.items.length : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
