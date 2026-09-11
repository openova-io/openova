import { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useParams } from 'react-router-dom'
import { api, errorText } from '../api/client'
import type { Estimate, PublicCatalog } from '../api/types'
import { Notice, Skeleton } from '../components/ui'
import { day } from '../lib/format'
import { formatMoney, formatPct } from '../lib/money'
import { useQuery } from '../lib/useQuery'
import {
  addLine,
  byService,
  cartReady,
  estimateBody,
  heightMessage,
  isEmbed,
  lineError,
  lineForPlan,
  lineForSKU,
  priceableLines,
  removeLine,
  searchCatalog,
  shareLink,
  updateLine,
  type CartLine,
} from './Estimate'

/**
 * The public cost calculator (DESIGN.md §11) — route `/estimate`, and
 * `/estimate/{id}` for a shared estimate. No sign-in, no console shell, no
 * authenticated call: the page reads GET /public/catalog and prices every
 * figure through POST /public/estimates, which rates with the same function
 * the invoices use. `?embed=1` drops the header and reports the page height
 * to the framing parent.
 *
 * Nothing on this page is a negotiated price: the catalog carries the one
 * list book the Sovereign published plus the catalog plans and the
 * pay-per-use rates, and the footer says so.
 */
export function EstimatePublic() {
  const { id } = useParams()
  const location = useLocation()
  const embed = isEmbed(location.search)
  const catalog = useQuery<PublicCatalog>('/public/catalog')
  const saved = useQuery<Estimate>(id ? `/public/estimates/${id}` : null)

  const [lines, setLines] = useState<CartLine[]>([])
  const [region, setRegion] = useState('')
  const [query, setQuery] = useState('')
  const [preview, setPreview] = useState<Estimate | null>(null)
  const [previewError, setPreviewError] = useState('')
  const [shared, setShared] = useState<Estimate | null>(null)
  const [email, setEmail] = useState('')
  const [sending, setSending] = useState(false)
  const [sent, setSent] = useState('')
  const [actionError, setActionError] = useState('')
  const root = useRef<HTMLDivElement>(null)

  const cat = catalog.data
  const currency = cat?.currency ?? ''
  const body = useMemo(() => estimateBody(lines, region), [lines, region])
  const bodyKey = JSON.stringify(body)

  // The live total. Every figure comes from the server: pricing lives in one
  // place (rating.PriceEstimate → rating.Rate), never in the browser.
  useEffect(() => {
    if (!cartReady(lines)) {
      setPreview(null)
      setPreviewError('')
      return
    }
    let cancelled = false
    const timer = setTimeout(() => {
      api
        .post<Estimate>('/public/estimates?preview=1', JSON.parse(bodyKey))
        .then((p) => {
          if (!cancelled) {
            setPreview(p)
            setPreviewError('')
          }
        })
        .catch((e: unknown) => {
          if (!cancelled) {
            setPreview(null)
            setPreviewError(errorText(e))
          }
        })
    }, 250)
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
    // bodyKey is the whole of the request; lines is read only for readiness.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bodyKey])

  // Framed: tell the parent how tall the document is, on every change.
  useEffect(() => {
    if (!embed || typeof window === 'undefined') return
    const post = () => {
      const h = root.current?.scrollHeight ?? document.documentElement.scrollHeight
      window.parent?.postMessage(heightMessage(h), '*')
    }
    post()
    const observer = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(post) : null
    if (observer && root.current) observer.observe(root.current)
    window.addEventListener('resize', post)
    return () => {
      observer?.disconnect()
      window.removeEventListener('resize', post)
    }
  })

  // A shared estimate is read-only: the saved document, as it was priced.
  if (id) {
    return (
      <div className="single-wide" ref={root}>
        {saved.loading && !saved.data ? <Skeleton lines={5} /> : null}
        {saved.error ? <Notice kind="bad">{saved.error}</Notice> : null}
        {saved.data ? (
          <div className="stack">
            {embed ? null : (
              <div className="page-head">
                <div>
                  <h1>Cost estimate</h1>
                  <div className="sub">
                    Prepared {day(saved.data.created_at)} · valid until {day(saved.data.valid_until)}
                    {saved.data.region ? ` · ${saved.data.region}` : ''}
                  </div>
                </div>
              </div>
            )}
            <EstimateTable estimate={saved.data} />
            <PriceFooter book={saved.data.price_book.name} at={saved.data.price_book.updated_at} notice={cat?.notice} />
          </div>
        ) : null}
      </div>
    )
  }

  const skus = searchCatalog(cat, query)
  const total = preview
  const save = async (withEmail: boolean) => {
    setSending(true)
    setActionError('')
    setSent('')
    try {
      const doc = await api.post<Estimate>('/public/estimates', estimateBody(lines, region, withEmail ? email : ''))
      setShared(doc)
      if (withEmail) setSent(`Sent to ${email.trim()} — we will be in touch.`)
    } catch (e: unknown) {
      setActionError(errorText(e))
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="single-wide" ref={root}>
      <div className="stack">
        {embed ? null : (
          <div className="page-head">
            <div>
              <h1>Cost calculator</h1>
              <div className="sub">
                Price a month of service at list prices. Tax is shown separately, and nothing here needs an account.
                {cat ? ` Prices as of ${day(cat.price_book.updated_at)}.` : ''}
              </div>
            </div>
          </div>
        )}

        {catalog.error ? <Notice kind="bad">{catalog.error}</Notice> : null}
        {catalog.loading && !cat ? (
          <div className="card">
            <Skeleton lines={4} />
          </div>
        ) : null}

        {cat ? (
          <div className="grid side">
            <div className="stack">
              {cat.plans.length ? (
                <div className="card">
                  <div className="card-head">
                    <h2>Plans</h2>
                    <span className="hint">a bundled Organization, billed monthly</span>
                  </div>
                  <div className="grid three">
                    {cat.plans.map((p) => (
                      <div key={p.slug} className="card flat">
                        <strong>{p.name}</strong>
                        <div className="muted small">
                          {p.vcpu} vCPU · {p.memory_gib} GiB
                        </div>
                        <div className="num" style={{ fontSize: 18, fontWeight: 650, margin: '4px 0' }}>
                          {formatMoney(p.monthly, currency)}
                          <span className="muted small"> / month</span>
                        </div>
                        <button onClick={() => setLines((l) => addLine(l, lineForPlan(p)))}>Add</button>
                      </div>
                    ))}
                  </div>
                </div>
              ) : null}

              <div className="card pad-0">
                <div className="card-head" style={{ padding: '14px 16px 0' }}>
                  <h2>Services</h2>
                  <input aria-label="Search the price list" placeholder="Search a service or SKU…" value={query} onChange={(e) => setQuery(e.target.value)} style={{ maxWidth: 260 }} />
                </div>
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>Service</th>
                        <th>Unit</th>
                        <th className="right">List price</th>
                        <th className="right">Per month</th>
                        <th />
                      </tr>
                    </thead>
                    <tbody>
                      {byService(skus).map((group) =>
                        group.skus.map((s, i) => (
                          <tr key={s.sku}>
                            <td>
                              {i === 0 ? <div className="tiny muted">{group.service}</div> : null}
                              <span className="mono">{s.sku}</span>
                              {s.description ? <div className="sub muted small">{s.description}</div> : null}
                            </td>
                            <td className="muted small">{s.unit}</td>
                            <td className="right num">{formatMoney(s.unit_price, '', { digits: 8 })}</td>
                            <td className="right num">{formatMoney(s.monthly, currency)}</td>
                            <td className="right">
                              <button className="small" onClick={() => setLines((l) => addLine(l, lineForSKU(s)))}>
                                Add
                              </button>
                            </td>
                          </tr>
                        )),
                      )}
                      {skus.length === 0 ? (
                        <tr>
                          <td colSpan={5} className="muted">
                            Nothing matches “{query}”.
                          </td>
                        </tr>
                      ) : null}
                    </tbody>
                  </table>
                </div>
              </div>

              {cat.payg.length ? (
                <div className="card pad-0">
                  <div className="card-head" style={{ padding: '14px 16px 0' }}>
                    <h2>Pay per use</h2>
                    <span className="hint">an uncapped Organization, billed on what it runs</span>
                  </div>
                  <div className="table-wrap">
                    <table>
                      <thead>
                        <tr>
                          <th>Meter</th>
                          <th>Unit</th>
                          <th className="right">Per month</th>
                          <th />
                        </tr>
                      </thead>
                      <tbody>
                        {cat.payg.map((r) => (
                          <tr key={r.sku}>
                            <td className="mono">{r.sku}</td>
                            <td className="muted small">{r.unit}</td>
                            <td className="right num">{formatMoney(r.monthly, currency)}</td>
                            <td className="right">
                              <button className="small" onClick={() => setLines((l) => addLine(l, lineForSKU(r)))}>
                                Add
                              </button>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </div>
              ) : null}
            </div>

            <div className="stack">
              <div className="card">
                <div className="card-head">
                  <h2>Your estimate</h2>
                  {cat.regions.length ? (
                    <select aria-label="Region" value={region} onChange={(e) => setRegion(e.target.value)} style={{ maxWidth: 180 }}>
                      <option value="">Any region</option>
                      {cat.regions.map((r) => (
                        <option key={r} value={r}>
                          {r}
                        </option>
                      ))}
                    </select>
                  ) : null}
                </div>

                {lines.length === 0 ? (
                  <p className="muted">Add a plan or a service to see what a month costs.</p>
                ) : (
                  <div className="stack tight">
                    {lines.map((l) => {
                      const err = lineError(l)
                      return (
                        <div key={l.key} className="card flat" style={{ padding: '8px 10px' }}>
                          <div className="row between">
                            <strong className={l.kind === 'plan' ? '' : 'mono'}>{l.label}</strong>
                            <button className="small danger" onClick={() => setLines((v) => removeLine(v, l.key))} aria-label={`Remove ${l.label}`}>
                              Remove
                            </button>
                          </div>
                          <div className="row">
                            <label className="tiny muted" htmlFor={`qty-${l.key}`}>
                              Quantity
                            </label>
                            <input id={`qty-${l.key}`} value={l.quantity} inputMode="decimal" onChange={(e) => setLines((v) => updateLine(v, l.key, { quantity: e.target.value }))} style={{ width: 90 }} />
                            {l.kind === 'plan' ? (
                              <>
                                <label className="tiny muted" htmlFor={`months-${l.key}`}>
                                  Months
                                </label>
                                <input id={`months-${l.key}`} value={l.months} inputMode="numeric" onChange={(e) => setLines((v) => updateLine(v, l.key, { months: e.target.value }))} style={{ width: 80 }} />
                              </>
                            ) : (
                              <>
                                <label className="tiny muted" htmlFor={`hours-${l.key}`}>
                                  Hours / month
                                </label>
                                <input id={`hours-${l.key}`} value={l.hours} inputMode="decimal" onChange={(e) => setLines((v) => updateLine(v, l.key, { hours: e.target.value }))} style={{ width: 90 }} />
                              </>
                            )}
                          </div>
                          {err ? <div className="tiny warn">{err}</div> : null}
                        </div>
                      )
                    })}
                  </div>
                )}

                {previewError ? <Notice kind="bad">{previewError}</Notice> : null}

                <div className="kv" style={{ marginTop: 10 }}>
                  <div className="row between">
                    <span className="muted">Subtotal</span>
                    <span className="num">{total ? formatMoney(total.subtotal, currency) : '—'}</span>
                  </div>
                  <div className="row between">
                    <span className="muted">Tax ({formatPct(Number(cat.tax_rate) * 100, { digits: 1 })})</span>
                    <span className="num">{total ? formatMoney(total.tax, currency) : '—'}</span>
                  </div>
                  <div className="row between">
                    <strong>Total for the term</strong>
                    <strong className="num">{total ? formatMoney(total.total, currency) : '—'}</strong>
                  </div>
                  <div className="row between">
                    <span className="muted">Per month</span>
                    <span className="num">{total ? formatMoney(total.monthly, currency) : '—'}</span>
                  </div>
                  <div className="row between">
                    <span className="muted">Per year</span>
                    <span className="num">{total ? formatMoney(total.yearly, currency) : '—'}</span>
                  </div>
                </div>

                <div className="row" style={{ marginTop: 12 }}>
                  <button className="primary" disabled={!cartReady(lines) || sending} onClick={() => void save(false)}>
                    Share estimate
                  </button>
                </div>
                {shared ? (
                  <div className="stack tight" style={{ marginTop: 8 }}>
                    <label className="tiny muted" htmlFor="share-link">
                      Anyone with this link can open the estimate — it quotes these prices until {day(shared.valid_until)}.
                    </label>
                    <input id="share-link" readOnly value={shareLink(shared, typeof window === 'undefined' ? '' : window.location.origin)} onFocus={(e) => e.currentTarget.select()} />
                  </div>
                ) : null}

                <div className="stack tight" style={{ marginTop: 12 }}>
                  <label className="tiny muted" htmlFor="lead-email">
                    Send me this estimate
                  </label>
                  <div className="row">
                    <input id="lead-email" type="email" placeholder="you@company.com" value={email} onChange={(e) => setEmail(e.target.value)} style={{ maxWidth: 220 }} />
                    <button disabled={!cartReady(lines) || !email.trim() || sending} onClick={() => void save(true)}>
                      Send
                    </button>
                  </div>
                </div>
                {sent ? <Notice kind="ok">{sent}</Notice> : null}
                {actionError ? <Notice kind="bad">{actionError}</Notice> : null}
                {priceableLines(lines).length ? <div className="tiny muted">{priceableLines(lines).length} priced line(s)</div> : null}
              </div>

              <PriceFooter book={cat.price_book.name} at={cat.price_book.updated_at} notice={cat.notice} />
            </div>
          </div>
        ) : null}
      </div>
    </div>
  )
}

/** The priced lines of a saved estimate, as the shared link shows them. */
function EstimateTable({ estimate }: { estimate: Estimate }) {
  return (
    <div className="card pad-0">
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Item</th>
              <th className="right">Quantity</th>
              <th className="right">Unit price</th>
              <th className="right">Amount</th>
            </tr>
          </thead>
          <tbody>
            {estimate.lines.map((l, i) => (
              <tr key={`${l.sku}-${i}`}>
                <td>
                  <span className={l.plan ? '' : 'mono'}>{l.plan ? `${l.plan.toUpperCase()} plan` : l.sku}</span>
                  <div className="sub muted small">
                    {l.quantity} × {l.plan ? `${l.months} month(s)` : `${l.hours} h`} · {l.unit}
                  </div>
                </td>
                <td className="right num">{l.rated_quantity}</td>
                <td className="right num">{formatMoney(l.unit_price, '', { digits: 8 })}</td>
                <td className="right num">{formatMoney(l.amount, estimate.currency)}</td>
              </tr>
            ))}
          </tbody>
          <tfoot>
            <tr>
              <td colSpan={3} className="right muted">
                Subtotal
              </td>
              <td className="right num">{formatMoney(estimate.subtotal, estimate.currency)}</td>
            </tr>
            <tr>
              <td colSpan={3} className="right muted">
                Tax ({formatPct(Number(estimate.tax_rate) * 100, { digits: 1 })})
              </td>
              <td className="right num">{formatMoney(estimate.tax, estimate.currency)}</td>
            </tr>
            <tr>
              <td colSpan={3} className="right">
                <strong>Total</strong>
              </td>
              <td className="right num">
                <strong>{formatMoney(estimate.total, estimate.currency)}</strong>
              </td>
            </tr>
            <tr>
              <td colSpan={3} className="right muted">
                Per month · per year
              </td>
              <td className="right num">
                {formatMoney(estimate.monthly, estimate.currency)} · {formatMoney(estimate.yearly, estimate.currency)}
              </td>
            </tr>
          </tfoot>
        </table>
      </div>
    </div>
  )
}

/** What the prices are, and when they were set. */
function PriceFooter({ book, at, notice }: { book: string; at?: string; notice?: string }) {
  return (
    <div className="card flat muted small">
      <div>
        Priced from <strong>{book}</strong>
        {at ? ` · prices as of ${day(at)}` : ''}.
      </div>
      <div>{notice ?? 'List prices. Taxes are shown separately. A negotiated price, a discount or a partner rate is never part of this estimate; contact us for a proposal.'}</div>
    </div>
  )
}
