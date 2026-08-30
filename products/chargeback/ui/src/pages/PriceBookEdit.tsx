import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { useParams } from 'react-router-dom'
import { api, asList, errorText } from '../api/client'
import type { PriceBook, PriceItem } from '../api/types'
import { Empty, Field, Notice } from '../components/ui'
import { PRICE_CSV_SAMPLE, dataUrl, parsePriceBookCsv, unitPrice, type PriceImportPreview } from '../lib/csv'
import { num } from '../lib/format'

export function PriceBookEdit() {
  const { id = '' } = useParams()
  const [book, setBook] = useState<PriceBook | null>(null)
  const [items, setItems] = useState<PriceItem[]>([])
  const [error, setError] = useState('')
  const [ok, setOk] = useState('')
  const [csv, setCsv] = useState('')
  const [csvFile, setCsvFile] = useState<File | null>(null)
  const [preview, setPreview] = useState<PriceImportPreview | null>(null)

  const load = useCallback(async () => {
    try {
      const b = await api.get<PriceBook>(`/pricebooks/${id}`)
      setBook(b)
      setItems(asList<PriceItem>(b.items ?? [], 'items'))
      setError('')
    } catch (e) {
      setError(errorText(e))
    }
  }, [id])
  useEffect(() => {
    void load()
  }, [load])

  if (error && !book) return <Notice kind="bad">{error}</Notice>
  if (!book) return <p className="muted">Loading…</p>

  const flash = (m: string) => {
    setOk(m)
    setError('')
  }

  const saveHeader = async (e: FormEvent) => {
    e.preventDefault()
    try {
      await api.put(`/pricebooks/${id}`, {
        name: book.name,
        currency: book.currency,
        annual_divisor: Number(book.annual_divisor),
        bill_stopped: book.bill_stopped,
        effective_from: book.effective_from || null,
      })
      flash('price book saved')
    } catch (err) {
      setError(errorText(err))
    }
  }

  const saveItems = async () => {
    try {
      const clean = items
        .map((it) => ({ ...it, sku: it.sku.trim(), unit: it.unit.trim(), unit_price: Number(it.unit_price) }))
        .filter((it) => it.sku)
      await api.put(`/pricebooks/${id}/items`, clean)
      flash(`${clean.length} item(s) saved`)
      await load()
    } catch (err) {
      setError(errorText(err))
    }
  }

  const importCsv = async () => {
    try {
      const form = new FormData()
      form.append('file', csvFile ?? new Blob([csv], { type: 'text/csv' }), 'pricebook.csv')
      await api.upload(`/pricebooks/${id}/import`, form)
      flash('CSV imported')
      setPreview(null)
      setCsv('')
      setCsvFile(null)
      await load()
    } catch (err) {
      setError(errorText(err))
    }
  }

  const update = (i: number, patch: Partial<PriceItem>) =>
    setItems((prev) => prev.map((it, j) => (j === i ? { ...it, ...patch } : it)))

  return (
    <div className="stack" style={{ maxWidth: 960 }}>
      <h1>{book.name}</h1>
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {ok ? <Notice kind="ok">{ok}</Notice> : null}

      <form className="card" onSubmit={saveHeader}>
        <div className="grid2">
          <Field label="Name">
            <input value={book.name} onChange={(e) => setBook({ ...book, name: e.target.value })} required />
          </Field>
          <Field label="Currency">
            <input value={book.currency} onChange={(e) => setBook({ ...book, currency: e.target.value.toUpperCase() })} maxLength={3} />
          </Field>
          <Field label="Annual divisor (hours per year)">
            <input
              type="number"
              min={1}
              value={book.annual_divisor}
              onChange={(e) => setBook({ ...book, annual_divisor: Number(e.target.value) })}
            />
          </Field>
          <Field label="Stopped compute is billed as">
            <select value={book.bill_stopped} onChange={(e) => setBook({ ...book, bill_stopped: e.target.value })}>
              <option value="compute">compute (full)</option>
              <option value="storage-only">storage-only</option>
              <option value="none">none</option>
            </select>
          </Field>
          <Field label="Effective from">
            <input type="date" value={book.effective_from ?? ''} onChange={(e) => setBook({ ...book, effective_from: e.target.value })} />
          </Field>
        </div>
        <button className="primary">Save</button>
      </form>

      <div className="row between">
        <h2>Items</h2>
        <div className="row">
          <button onClick={() => setItems((p) => [...p, { sku: '', unit: 'hour', unit_price: 0, description: '' }])}>Add row</button>
          <button className="primary" onClick={() => void saveItems()}>
            Save items
          </button>
        </div>
      </div>
      {items.length === 0 ? (
        <Empty>No items. Add rows or import a CSV below.</Empty>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>SKU</th>
                <th>Unit</th>
                <th className="num">Unit price ({book.currency})</th>
                <th>Description</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {items.map((it, i) => (
                <tr key={i}>
                  <td>
                    <input className="mono" value={it.sku} onChange={(e) => update(i, { sku: e.target.value })} />
                  </td>
                  <td>
                    <input value={it.unit} onChange={(e) => update(i, { unit: e.target.value })} />
                  </td>
                  <td>
                    <input type="number" step="0.00000001" min={0} value={it.unit_price} onChange={(e) => update(i, { unit_price: e.target.value })} />
                  </td>
                  <td>
                    <input value={it.description ?? ''} onChange={(e) => update(i, { description: e.target.value })} />
                  </td>
                  <td>
                    <button className="link" onClick={() => setItems((p) => p.filter((_, j) => j !== i))}>
                      Remove
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="row between">
        <h2>Import CSV</h2>
        <a href={dataUrl(PRICE_CSV_SAMPLE)} download="pricebook-sample.csv">
          Download sample CSV
        </a>
      </div>
      <div className="card stack">
        <p className="muted small">
          Columns: <code>sku,unit,annual_price,description</code>. The unit price is annual_price ÷ {book.annual_divisor}.
          Mapping the National Cloud catalogue onto SKUs is done in this file.
        </p>
        <input
          type="file"
          accept=".csv,text/csv"
          onChange={(e) => {
            const f = e.target.files?.[0] ?? null
            setCsvFile(f)
            if (f) void f.text().then((t) => setCsv(t))
          }}
        />
        <textarea value={csv} onChange={(e) => setCsv(e.target.value)} placeholder="…or paste CSV here" />
        <div className="row">
          <button onClick={() => setPreview(parsePriceBookCsv(csv))}>Preview</button>
          <button className="primary" disabled={!preview || preview.rows.length === 0} onClick={() => void importCsv()}>
            Import {preview ? preview.rows.length : 0} row(s)
          </button>
        </div>
        {preview ? (
          <>
            {preview.errors.length ? (
              <Notice kind="warn">
                {preview.errors.length} line(s) have problems:
                <ul style={{ margin: '4px 0 0 18px' }}>
                  {preview.errors.map((e) => (
                    <li key={e.line}>
                      line {e.line}: {e.message}
                    </li>
                  ))}
                </ul>
              </Notice>
            ) : null}
            {preview.rows.length ? (
              <table>
                <thead>
                  <tr>
                    <th>SKU</th>
                    <th>Unit</th>
                    <th className="num">Annual</th>
                    <th className="num">Unit price</th>
                    <th>Description</th>
                  </tr>
                </thead>
                <tbody>
                  {preview.rows.map((r) => (
                    <tr key={r.line}>
                      <td className="mono">{r.sku}</td>
                      <td>{r.unit}</td>
                      <td className="num">{num(r.annual_price, 3)}</td>
                      <td className="num">{num(unitPrice(r.annual_price, book.annual_divisor), 8)}</td>
                      <td>{r.description}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : null}
          </>
        ) : null}
      </div>
    </div>
  )
}
