import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import type { CurrencyRate, CurrencyRates } from '../api/types'
import { describeRate, rateBody, readRates, validateRate, type RateDraft, type RateErrors } from '../lib/currencies'
import { when } from '../lib/format'
import { formatNumber } from '../lib/money'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'
import { Confirm, Notice, Skeleton } from './ui'

/**
 * Currencies card (DESIGN.md §2.5 / §3.10): the reporting currency (read
 * from the allocation settings, changed there) and the exchange rates every
 * cost screen converts price-book currencies with — one row per currency,
 * added, edited and deleted in place against /currencies/{code}.
 */
export function CurrencyRatesCard() {
  const q = useQuery<CurrencyRates>('/currencies')
  const { reporting, rates } = readRates(q.data)
  const act = useAction()
  // One row is editable at a time: an existing code, or '' for the new row.
  const [editing, setEditing] = useState<string | null>(null)
  const [draft, setDraft] = useState<RateDraft>({ code: '', per_base: '' })
  const [errors, setErrors] = useState<RateErrors>({})
  const [removing, setRemoving] = useState<CurrencyRate | null>(null)

  const open = (r?: CurrencyRate) => {
    act.clear()
    setErrors({})
    setEditing(r ? r.code : '')
    setDraft(r ? { code: r.code, per_base: String(r.per_base) } : { code: '', per_base: '' })
  }
  const close = () => {
    setEditing(null)
    setErrors({})
  }
  const save = async () => {
    const errs = validateRate(draft, reporting, rates.map((r) => r.code), editing || undefined)
    setErrors(errs)
    if (errs.code || errs.per_base) return
    const code = draft.code.trim().toUpperCase()
    const ok = await act.run(`${code} rate saved — every cost screen now converts ${code} at ${draft.per_base.trim()} per ${reporting}`, () => api.put(`/currencies/${code}`, rateBody(draft)), q.reload)
    if (ok) close()
  }
  const remove = async (r: CurrencyRate) => {
    const ok = await act.run(`${r.code} rate removed — ${r.code} usage is unconverted until a rate is added again`, () => api.del(`/currencies/${r.code}`), q.reload)
    if (ok) setRemoving(null)
  }

  const editRow = (key: string) => (
    <tr key={key} className="editing">
      <td>
        <input
          value={draft.code}
          onChange={(e) => setDraft((d) => ({ ...d, code: e.target.value.toUpperCase() }))}
          maxLength={3}
          className="mono"
          placeholder="USD"
          aria-label="Currency code"
          aria-invalid={Boolean(errors.code)}
          readOnly={editing !== ''}
          autoFocus={editing === ''}
          style={{ width: 80 }}
        />
        {errors.code ? <div className="err">{errors.code}</div> : null}
      </td>
      <td className="num">
        <input
          value={draft.per_base}
          onChange={(e) => setDraft((d) => ({ ...d, per_base: e.target.value }))}
          inputMode="decimal"
          placeholder="2.6"
          aria-label={`Units per ${reporting}`}
          aria-invalid={Boolean(errors.per_base)}
          autoFocus={editing !== ''}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              void save()
            }
            if (e.key === 'Escape') close()
          }}
          style={{ width: 140 }}
        />
        {errors.per_base ? <div className="err">{errors.per_base}</div> : null}
      </td>
      <td className="num muted">{validateRate(draft, reporting).per_base ? '—' : describeRate({ code: draft.code.trim().toUpperCase() || '?', per_base: Number(draft.per_base) }, reporting).split(' · ')[1]}</td>
      <td className="muted">manual</td>
      <td className="muted">{editing ? 'on save' : 'now'}</td>
      <td className="actions">
        <span className="btn-row">
          <button className="small primary" onClick={() => void save()} disabled={act.busy}>
            {act.busy ? 'Saving…' : 'Save'}
          </button>
          <button className="small" onClick={close} disabled={act.busy}>
            Cancel
          </button>
        </span>
      </td>
    </tr>
  )

  return (
    <div className="card">
      <div className="card-head">
        <h2>Currencies</h2>
        <span className="hint">
          reporting currency <b className="mono">{reporting || '…'}</b> · <Link to="/allocation">change it in Allocation settings</Link>
        </span>
      </div>
      <p className="muted small" style={{ marginTop: 0 }}>
        Every cost screen reports in <b>{reporting || 'the reporting currency'}</b>. A price book in another currency is converted at the rate below — <i>per {reporting || 'base'}</i> is how many units of that currency one {reporting || 'reporting unit'}{' '}
        buys — and a currency without a rate is listed as unconverted and left out of every total. Statements are issued in the book's own currency and are never converted.
      </p>
      {q.error ? <Notice kind="bad">{q.error}</Notice> : null}
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {q.loading && !q.data ? (
        <Skeleton lines={2} />
      ) : (
        <table>
          <thead>
            <tr>
              <th>Code</th>
              <th className="num">Per {reporting || 'base'}</th>
              <th className="num">One unit in {reporting || 'base'}</th>
              <th>Source</th>
              <th>Updated</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rates.map((r) =>
              editing === r.code ? (
                editRow(r.code)
              ) : (
                <tr key={r.code}>
                  <td className="mono">{r.code}</td>
                  <td className="num tabular">{formatNumber(r.per_base, 10)}</td>
                  <td className="num tabular" title={describeRate(r, reporting)}>
                    {formatNumber(1 / r.per_base, 6)} {reporting}
                  </td>
                  <td>{r.source}</td>
                  <td className="nowrap">{when(r.updated_at)}</td>
                  <td className="actions">
                    <span className="btn-row">
                      <button className="small" onClick={() => open(r)} disabled={act.busy || editing !== null}>
                        Edit
                      </button>
                      <button className="small danger" onClick={() => setRemoving(r)} disabled={act.busy || editing !== null}>
                        Delete
                      </button>
                    </span>
                  </td>
                </tr>
              ),
            )}
            {editing === '' ? editRow('new') : null}
            {rates.length === 0 && editing === null ? (
              <tr>
                <td colSpan={6} className="muted">
                  No exchange rates yet. Price books in {reporting || 'the reporting currency'} need none; add a rate for every other book currency in use, or its usage stays unconverted.
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      )}
      <div className="row end" style={{ marginTop: 8 }}>
        <button className="small" onClick={() => open()} disabled={act.busy || editing !== null || !reporting}>
          Add currency
        </button>
      </div>
      {removing ? (
        <Confirm
          title={`Remove the ${removing.code} rate?`}
          danger
          confirmLabel="Remove rate"
          busy={act.busy}
          onClose={() => setRemoving(null)}
          onConfirm={() => remove(removing)}
          body={
            <>
              Usage priced in <b>{removing.code}</b> will no longer be converted to {reporting}: it drops out of every total and is listed as unconverted on the overview, the explorer and reports until a rate is added again. Price books and statements are not affected.
            </>
          }
        />
      ) : null}
    </div>
  )
}
