import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api, errorText } from '../api/client'
import type { ImportResult } from '../api/types'
import { Notice } from '../components/ui'
import { CUSTOMER_CSV_SAMPLE, dataUrl, parseCustomersCsv, type CustomerImportPreview } from '../lib/csv'

/**
 * Bulk customer import (spec §4 POST /customers/import). The CSV is parsed
 * and validated in the browser first so the preview shows exactly what
 * will be sent; the valid rows are then posted as a JSON array.
 */
export function CustomerImport() {
  const [text, setText] = useState('')
  const [preview, setPreview] = useState<CustomerImportPreview | null>(null)
  const [result, setResult] = useState<ImportResult | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const onFile = (f: File | undefined) => {
    if (!f) return
    f.text().then((t) => {
      setText(t)
      setPreview(parseCustomersCsv(t))
      setResult(null)
    })
  }

  const send = async () => {
    if (!preview || preview.rows.length === 0) return
    setBusy(true)
    setError('')
    try {
      const body = preview.rows.map(({ line: _line, ...row }) => row)
      setResult(await api.post<ImportResult>('/customers/import', body))
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="stack" style={{ maxWidth: 960 }}>
      <div className="row between">
        <h1>Import customers</h1>
        <a href={dataUrl(CUSTOMER_CSV_SAMPLE)} download="customers-sample.csv">
          Download sample CSV
        </a>
      </div>
      <div className="card stack">
        <p className="muted small">
          Columns: <code>slug,name,admin_email,region,project_ids,price_book,billing_mode,start_date</code> — several
          project ids are separated by <code>;</code>. Existing slugs are updated; new slugs are created pending.
        </p>
        <input type="file" accept=".csv,text/csv" onChange={(e) => onFile(e.target.files?.[0])} />
        <textarea value={text} onChange={(e) => setText(e.target.value)} placeholder="…or paste CSV here" />
        <div className="row">
          <button
            onClick={() => {
              setPreview(parseCustomersCsv(text))
              setResult(null)
            }}
          >
            Preview
          </button>
          <button className="primary" disabled={busy || !preview || preview.rows.length === 0} onClick={() => void send()}>
            Import {preview ? preview.rows.length : 0} valid row(s)
          </button>
        </div>
      </div>

      {preview ? (
        <div className="stack">
          {preview.errors.length ? (
            <Notice kind="warn">
              {preview.errors.length} line(s) will be skipped:
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
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Line</th>
                    <th>Slug</th>
                    <th>Name</th>
                    <th>Admin email</th>
                    <th>Region</th>
                    <th>Projects</th>
                    <th>Price book</th>
                    <th>Billing</th>
                    <th>Start</th>
                  </tr>
                </thead>
                <tbody>
                  {preview.rows.map((r) => (
                    <tr key={r.line}>
                      <td>{r.line}</td>
                      <td className="mono">{r.slug}</td>
                      <td>{r.name}</td>
                      <td>{r.admin_email}</td>
                      <td>{r.region || '—'}</td>
                      <td className="mono small">{r.project_ids.join(', ') || '—'}</td>
                      <td>{r.price_book || '—'}</td>
                      <td>{r.billing_mode}</td>
                      <td>{r.start_date || '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : null}
        </div>
      ) : null}

      {error ? <Notice kind="bad">{error}</Notice> : null}
      {result ? (
        <Notice kind={result.errors && result.errors.length ? 'warn' : 'ok'}>
          Created {result.created}, updated {result.updated}
          {result.errors && result.errors.length ? `, ${result.errors.length} error(s)` : ''}.{' '}
          <Link to="/customers">Back to customers</Link>
          {result.errors && result.errors.length ? (
            <ul style={{ margin: '4px 0 0 18px' }}>
              {result.errors.map((e, i) => (
                <li key={i}>{typeof e === 'string' ? e : `${e.line ? `line ${e.line}: ` : ''}${e.slug ? `${e.slug}: ` : ''}${e.message}`}</li>
              ))}
            </ul>
          ) : null}
        </Notice>
      ) : null}
    </div>
  )
}
