import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api, errorText } from '../api/client'
import type { ImportResult } from '../api/types'
import { Notice } from '../components/ui'
import { t } from '../i18n'
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
        <h1>{t('customerImport.title')}</h1>
        <a href={dataUrl(CUSTOMER_CSV_SAMPLE)} download="customers-sample.csv">
          {t('customerImport.downloadSample')}
        </a>
      </div>
      <div className="card stack">
        <p className="muted small">
          {t('customerImport.columns')} <code>slug,name,admin_email,region,project_ids,price_book,billing_mode,start_date</code>{' '}
          {t('customerImport.columnsSeparator')} <code>;</code>
          {t('customerImport.columnsTail')}
        </p>
        <input type="file" accept=".csv,text/csv" onChange={(e) => onFile(e.target.files?.[0])} />
        <textarea value={text} onChange={(e) => setText(e.target.value)} placeholder={t('customerImport.paste')} />
        <div className="row">
          <button
            onClick={() => {
              setPreview(parseCustomersCsv(text))
              setResult(null)
            }}
          >
            {t('customerImport.preview')}
          </button>
          <button className="primary" disabled={busy || !preview || preview.rows.length === 0} onClick={() => void send()}>
            {t('customerImport.import', { count: preview ? preview.rows.length : 0 })}
          </button>
        </div>
      </div>

      {preview ? (
        <div className="stack">
          {preview.errors.length ? (
            <Notice kind="warn">
              {t('customerImport.skipped', { count: preview.errors.length })}
              <ul style={{ margin: '4px 0 0 18px' }}>
                {preview.errors.map((e) => (
                  <li key={e.line}>{t('customerImport.lineError', { line: e.line, message: e.message })}</li>
                ))}
              </ul>
            </Notice>
          ) : null}
          {preview.rows.length ? (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>{t('customerImport.col.line')}</th>
                    <th>{t('customerImport.col.slug')}</th>
                    <th>{t('customerImport.col.name')}</th>
                    <th>{t('customerImport.col.adminEmail')}</th>
                    <th>{t('common.region')}</th>
                    <th>{t('customerImport.col.projects')}</th>
                    <th>{t('customerImport.col.priceBook')}</th>
                    <th>{t('customerImport.col.billing')}</th>
                    <th>{t('customerImport.col.start')}</th>
                  </tr>
                </thead>
                <tbody>
                  {preview.rows.map((r) => (
                    <tr key={r.line}>
                      <td>{r.line}</td>
                      <td className="mono">{r.slug}</td>
                      <td>{r.name}</td>
                      <td>{r.admin_email}</td>
                      <td>{r.region || t('common.none')}</td>
                      <td className="mono small">{r.project_ids.join(', ') || t('common.none')}</td>
                      <td>{r.price_book || t('common.none')}</td>
                      <td>{r.billing_mode}</td>
                      <td>{r.start_date || t('common.none')}</td>
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
          {t('customerImport.created', { created: result.created, updated: result.updated })}
          {result.errors && result.errors.length ? t('customerImport.errorCount', { count: result.errors.length }) : ''}.{' '}
          <Link to="/customers">{t('customerImport.back')}</Link>
          {result.errors && result.errors.length ? (
            <ul style={{ margin: '4px 0 0 18px' }}>
              {result.errors.map((e, i) => (
                <li key={i}>{typeof e === 'string' ? e : [e.line ? t('customerImport.lineNo', { line: e.line }) : '', e.slug ?? '', e.message].filter(Boolean).join(': ')}</li>
              ))}
            </ul>
          ) : null}
        </Notice>
      ) : null}
    </div>
  )
}
