import { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useParams } from 'react-router-dom'
import { api, errorText } from '../api/client'
import type { Estimate, PublicCatalog } from '../api/types'
import { Notice, Skeleton } from '../components/ui'
import { day } from '../lib/format'
import { formatMoney, formatPct } from '../lib/money'
import { useQuery } from '../lib/useQuery'
import { Catalogue } from '../panels/estimate/Catalogue'
import { Configurator } from '../panels/estimate/Configurator'
import { Summary } from '../panels/estimate/Summary'
import {
  buildCatalogue,
  catalogEntries,
  estimateReady,
  findService,
  makeItem,
  pricedByItem,
  removeItem,
  requestBody,
  searchCatalogue,
  upsertItem,
  type CatalogService,
  type EstimateItem,
  type ItemConfig,
} from '../panels/estimate/model'
import { heightMessage, isEmbed, shareLink } from './Estimate'

/**
 * The public cost calculator (DESIGN.md §12) — route `/estimate`, and
 * `/estimate/{id}` for a shared estimate. No sign-in, no console shell, no
 * authenticated call: the page reads GET /public/catalog and prices every
 * figure through POST /public/estimates, which rates with the same function
 * the invoices use. `?embed=1` drops the header and reports the page height
 * to the framing parent.
 *
 * The page is the shape of the AWS and Azure calculators: a service
 * catalogue by product family on the left, a configurator per service (a
 * modal of dropdowns and numbers, never a SKU), and the estimate on the
 * right — items grouped by service, each with its lines, Edit and Remove,
 * the totals, the share link and the lead capture. Nothing on this page is
 * a negotiated price: the catalog carries the one list book the Sovereign
 * published plus the catalog plans and the pay-per-use rates, and the
 * footer says so.
 */
export function EstimatePublic() {
  const { id } = useParams()
  const location = useLocation()
  const embed = isEmbed(location.search)
  const catalog = useQuery<PublicCatalog>('/public/catalog')
  const saved = useQuery<Estimate>(id ? `/public/estimates/${id}` : null)

  const [items, setItems] = useState<EstimateItem[]>([])
  const [region, setRegion] = useState('')
  const [query, setQuery] = useState('')
  const [editing, setEditing] = useState<{ service: CatalogService; item: EstimateItem | null } | null>(null)
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [preview, setPreview] = useState<Estimate | null>(null)
  const [previewError, setPreviewError] = useState('')
  const [shared, setShared] = useState<Estimate | null>(null)
  const [email, setEmail] = useState('')
  const [sending, setSending] = useState(false)
  const [sent, setSent] = useState('')
  const [actionError, setActionError] = useState('')
  const root = useRef<HTMLDivElement>(null)
  const nextId = useRef(0)

  const cat = catalog.data
  const currency = cat?.currency ?? ''
  const families = useMemo(() => buildCatalogue(cat), [cat])
  const shown = useMemo(() => searchCatalogue(families, query), [families, query])
  const bodyKey = useMemo(() => JSON.stringify(requestBody(items, region).body), [items, region])
  const ready = estimateReady(items)

  // The live total. Every figure comes from the server: pricing lives in one
  // place (rating.PriceEstimate → rating.Rate), never in the browser.
  useEffect(() => {
    if (!ready) {
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
  }, [bodyKey, ready])

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
            <EstimateTable estimate={saved.data} catalog={cat} />
            <PriceFooter book={saved.data.price_book.name} at={saved.data.price_book.updated_at} notice={cat?.notice} />
          </div>
        ) : null}
      </div>
    )
  }

  const priced = pricedByItem(items, preview)
  const commit = (config: ItemConfig) => {
    if (!editing) return
    const itemId = editing.item?.id ?? `i${++nextId.current}`
    setItems((v) => upsertItem(v, makeItem(cat, itemId, config)))
    setExpanded((v) => ({ ...v, [itemId]: true }))
    setEditing(null)
    setShared(null)
    setSent('')
  }
  const edit = (item: EstimateItem) => {
    const service = findService(cat, item.service)
    if (service) setEditing({ service, item })
  }
  const remove = (itemId: string) => {
    setItems((v) => removeItem(v, itemId))
    setShared(null)
    setSent('')
  }
  const save = async (withEmail: boolean) => {
    setSending(true)
    setActionError('')
    setSent('')
    try {
      const doc = await api.post<Estimate>('/public/estimates', requestBody(items, region, withEmail ? email : '').body)
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
                Choose the services you need and see what a month costs, at list prices. Tax is shown separately, and nothing here needs an account.
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
          <>
            {cat.regions.length ? (
              <div className="toolbar">
                <div className="field">
                  <label htmlFor="estimate-region">Region</label>
                  <select id="estimate-region" value={region} onChange={(e) => setRegion(e.target.value)} style={{ minWidth: 200 }}>
                    <option value="">Any region</option>
                    {cat.regions.map((r) => (
                      <option key={r} value={r}>
                        {r}
                      </option>
                    ))}
                  </select>
                </div>
                <div className="grow" />
                <span className="small muted">The region applies to every item. Prices are the same in every region of this list.</span>
              </div>
            ) : null}

            <div className="grid side">
              <Catalogue families={shown} currency={currency} query={query} onQuery={setQuery} onChoose={(service) => setEditing({ service, item: null })} />

              <div className="stack">
                <Summary
                  items={items}
                  priced={priced}
                  currency={currency}
                  taxRate={cat.tax_rate}
                  totals={preview}
                  previewError={previewError}
                  expanded={expanded}
                  onToggle={(itemId) => setExpanded((v) => ({ ...v, [itemId]: !v[itemId] }))}
                  onEdit={edit}
                  onRemove={remove}
                >
                  <div className="row" style={{ marginTop: 12 }}>
                    <button className="primary" disabled={!ready || sending} onClick={() => void save(false)}>
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
                      <button disabled={!ready || !email.trim() || sending} onClick={() => void save(true)}>
                        Send
                      </button>
                    </div>
                  </div>
                  {sent ? <Notice kind="ok">{sent}</Notice> : null}
                  {actionError ? <Notice kind="bad">{actionError}</Notice> : null}
                </Summary>

                <PriceFooter book={cat.price_book.name} at={cat.price_book.updated_at} notice={cat.notice} />
              </div>
            </div>

            {editing ? <Configurator key={editing.item?.id ?? `new-${editing.service.key}`} catalog={cat} service={editing.service} initial={editing.item?.config ?? null} onClose={() => setEditing(null)} onSubmit={commit} /> : null}
          </>
        ) : null}
      </div>
    </div>
  )
}

/** The priced lines of a saved estimate, as the shared link shows them — named from the catalog when it is at hand. */
function EstimateTable({ estimate, catalog }: { estimate: Estimate; catalog: PublicCatalog | null }) {
  const names = new Map<string, string>()
  if (catalog) for (const e of catalogEntries(catalog)) names.set(e.sku, e.display_name)
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
            {estimate.lines.map((l, i) => {
              const name = l.plan ? `${l.plan.toUpperCase()} plan` : names.get(l.sku)
              return (
                <tr key={`${l.sku}-${i}`}>
                  <td>
                    {name ? (
                      <>
                        {name} <span className="mono muted tiny">{l.sku}</span>
                      </>
                    ) : (
                      <span className="mono">{l.sku}</span>
                    )}
                    <div className="sub muted small">
                      {l.quantity} × {l.plan ? `${l.months} month(s)` : `${l.hours} h`} · {l.unit}
                    </div>
                  </td>
                  <td className="right num">{l.rated_quantity}</td>
                  <td className="right num">{formatMoney(l.unit_price, '', { digits: 8 })}</td>
                  <td className="right num">{formatMoney(l.amount, estimate.currency)}</td>
                </tr>
              )
            })}
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
                Per month · 12 months
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
