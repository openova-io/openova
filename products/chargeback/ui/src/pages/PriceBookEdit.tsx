import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { API_BASE, api, asList, errorText } from '../api/client'
import type { PriceBook, PriceBookCoverage, PriceItem } from '../api/types'
import { DataTable, sortRows, type Column } from '../components/DataTable'
import { BookSettingsModal, CloneBookModal, DeleteBookConfirm, billStoppedLabel, settingsFrom } from '../components/PriceBookForms'
import { Badge, Confirm, EmptyState, Field, KPI, Modal, Notice, PageHeader, Skeleton } from '../components/ui'
import { PRICE_CSV_SAMPLE, dataUrl, parsePriceBookCsv, unitPrice } from '../lib/csv'
import { day, num } from '../lib/format'
import { formatMoney, formatPct, formatQty } from '../lib/money'
import { round, toNumber } from '../lib/num'
import { useQuery } from '../lib/useQuery'

/**
 * Price book detail (DESIGN.md §2.5): settings, coverage of the SKUs in use,
 * and the item list with per-row CRUD — PATCH /items/{sku} to save a row,
 * DELETE /items/{sku} to remove one, POST /items to add one. Annual and
 * hourly prices are two views of one number: unit_price = annual ÷ divisor.
 */

interface Draft {
  unit: string
  unit_price: string
  annual_price: string
  description: string
}
type AddDraft = Draft & { sku: string }

type Dialog = { kind: 'settings' } | { kind: 'import' } | { kind: 'clone' } | { kind: 'delete' } | { kind: 'delete-item'; item: PriceItem } | null

function annualOf(it: PriceItem, divisor: number): number {
  return it.annual_price !== null && it.annual_price !== undefined && it.annual_price !== '' ? toNumber(it.annual_price) : round(toNumber(it.unit_price) * divisor, 6)
}

function draftOf(it: PriceItem, divisor: number): Draft {
  return { unit: it.unit, unit_price: String(toNumber(it.unit_price)), annual_price: String(annualOf(it, divisor)), description: it.description ?? '' }
}

function sameDraft(a: Draft, b: Draft): boolean {
  return a.unit === b.unit && a.unit_price === b.unit_price && a.annual_price === b.annual_price && a.description === b.description
}

/** Keep annual and hourly in step: editing one recomputes the other. */
function linked(d: Draft, patch: Partial<Draft>, divisor: number): Draft {
  const next = { ...d, ...patch }
  if (patch.annual_price !== undefined) next.unit_price = patch.annual_price === '' ? '' : String(unitPrice(Number(patch.annual_price), divisor))
  else if (patch.unit_price !== undefined) next.annual_price = patch.unit_price === '' ? '' : String(round(Number(patch.unit_price) * divisor, 6))
  return next
}

function itemBody(d: Draft) {
  return { unit: d.unit.trim(), unit_price: Number(d.unit_price), annual_price: Number(d.annual_price), description: d.description.trim() }
}

function validDraft(d: Draft): string {
  if (!d.unit.trim()) return 'unit is required'
  const u = Number(d.unit_price)
  if (d.unit_price === '' || !Number.isFinite(u) || u < 0) return 'unit price must be a non-negative number'
  return ''
}

export function PriceBookEdit() {
  const { id = '' } = useParams()
  const nav = useNavigate()
  const book = useQuery<PriceBook>(`/pricebooks/${id}`)
  const coverage = useQuery<PriceBookCoverage>(`/pricebooks/${id}/coverage`)
  const [dialog, setDialog] = useState<Dialog>(null)
  const [flash, setFlash] = useState('')
  const [error, setError] = useState('')
  const [q, setQ] = useState('')
  const [sort, setSort] = useState<{ key: string; dir: 'asc' | 'desc' }>({ key: 'sku', dir: 'asc' })
  const [edits, setEdits] = useState<Record<string, Draft>>({})
  const [rowErr, setRowErr] = useState<Record<string, string>>({})
  const [busySku, setBusySku] = useState<string | null>(null)
  const [add, setAdd] = useState<AddDraft | null>(null)
  const [addErr, setAddErr] = useState('')
  const [addBusy, setAddBusy] = useState(false)
  const addRef = useRef<HTMLFormElement | null>(null)

  const b = book.data
  const divisor = b?.annual_divisor && b.annual_divisor > 0 ? b.annual_divisor : 8760
  const currency = b?.currency ?? ''
  const items = useMemo(() => asList<PriceItem>(b?.items ?? [], 'items'), [b])

  const reload = useCallback(async () => {
    await Promise.all([book.reload(), coverage.reload()])
  }, [book, coverage])

  const ok = (m: string) => {
    setFlash(m)
    setError('')
  }

  // Item CRUD ────────────────────────────────────────────────────────────
  const setDraft = (sku: string, patch: Partial<Draft>) =>
    setEdits((prev) => {
      const it = items.find((x) => x.sku === sku)
      const base = prev[sku] ?? (it ? draftOf(it, divisor) : { unit: '', unit_price: '', annual_price: '', description: '' })
      return { ...prev, [sku]: linked(base, patch, divisor) }
    })
  const revert = (sku: string) =>
    setEdits((prev) => {
      const { [sku]: _gone, ...rest } = prev
      return rest
    })
  const saveRow = async (it: PriceItem) => {
    const d = edits[it.sku]
    if (!d) return
    const problem = validDraft(d)
    if (problem) {
      setRowErr((p) => ({ ...p, [it.sku]: problem }))
      return
    }
    setBusySku(it.sku)
    setRowErr((p) => ({ ...p, [it.sku]: '' }))
    try {
      await api.patch(`/pricebooks/${id}/items/${encodeURIComponent(it.sku)}`, itemBody(d))
      revert(it.sku)
      ok(`${it.sku} saved`)
      await reload()
    } catch (e) {
      setRowErr((p) => ({ ...p, [it.sku]: errorText(e) }))
    } finally {
      setBusySku(null)
    }
  }
  const deleteRow = async (it: PriceItem) => {
    setBusySku(it.sku)
    try {
      await api.del(`/pricebooks/${id}/items/${encodeURIComponent(it.sku)}`)
      setDialog(null)
      revert(it.sku)
      ok(`${it.sku} removed`)
      await reload()
    } catch (e) {
      setError(errorText(e))
      setDialog(null)
    } finally {
      setBusySku(null)
    }
  }
  const openAdd = (prefill?: Partial<AddDraft>) => {
    setAdd({ sku: '', unit: 'hour', unit_price: '', annual_price: '', description: '', ...prefill })
    setAddErr('')
    window.setTimeout(() => addRef.current?.scrollIntoView({ behavior: 'smooth', block: 'center' }), 0)
  }
  const submitAdd = async (e: FormEvent) => {
    e.preventDefault()
    if (!add) return
    const sku = add.sku.trim()
    if (!sku) {
      setAddErr('SKU is required')
      return
    }
    if (items.some((it) => it.sku === sku)) {
      setAddErr(`${sku} is already in this book — edit its row instead`)
      return
    }
    const problem = validDraft(add)
    if (problem) {
      setAddErr(problem)
      return
    }
    setAddBusy(true)
    setAddErr('')
    try {
      await api.post(`/pricebooks/${id}/items`, { sku, ...itemBody(add) })
      setAdd(null)
      ok(`${sku} added`)
      await reload()
    } catch (err) {
      setAddErr(errorText(err))
    } finally {
      setAddBusy(false)
    }
  }

  // Items table (sortable, searchable) ────────────────────────────────────
  const sortCols: Column<PriceItem>[] = [
    { key: 'sku', header: 'SKU', value: (r) => r.sku },
    { key: 'description', header: 'Description', value: (r) => r.description ?? '' },
    { key: 'unit', header: 'Unit', value: (r) => r.unit },
    { key: 'annual', header: `Annual price (${currency}/yr)`, value: (r) => annualOf(r, divisor), numeric: true },
    { key: 'unit_price', header: `Unit price (${currency}/unit)`, value: (r) => toNumber(r.unit_price), numeric: true },
  ]
  const needle = q.trim().toLowerCase()
  const visible = useMemo(() => {
    const filtered = needle ? items.filter((it) => it.sku.toLowerCase().includes(needle) || (it.description ?? '').toLowerCase().includes(needle)) : items
    return sortRows(
      filtered,
      sortCols.find((c) => c.key === sort.key),
      sort.dir,
    )
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items, needle, sort, divisor])
  const toggleSort = (c: Column<PriceItem>) => setSort((s) => (s.key === c.key ? { key: c.key, dir: s.dir === 'asc' ? 'desc' : 'asc' } : { key: c.key, dir: c.numeric ? 'desc' : 'asc' }))

  // Coverage table ─────────────────────────────────────────────────────────
  type CovRow = PriceBookCoverage['skus_in_use'][number]
  const covRows = useMemo(() => [...(coverage.data?.skus_in_use ?? [])].sort((a, b) => b.quantity_30d - a.quantity_30d), [coverage.data])
  const covCols: Column<CovRow>[] = [
    { key: 'priced', header: 'Rate', value: (r) => (r.priced ? 1 : 0), render: (r) => (r.priced ? <Badge status="priced" kind="ok" /> : <Badge status="unpriced" kind="warn" />) },
    { key: 'sku', header: 'SKU', value: (r) => r.sku, render: (r) => <span className="mono">{r.sku}</span> },
    { key: 'unit', header: 'Unit', value: (r) => r.unit },
    { key: 'qty', header: 'Quantity, 30 d', value: (r) => r.quantity_30d, numeric: true, render: (r) => formatQty(r.quantity_30d, r.unit) },
    { key: 'resources', header: 'Resources', value: (r) => r.resources, numeric: true },
    {
      key: 'price',
      header: `Unit price (${currency})`,
      value: (r) => r.unit_price,
      numeric: true,
      render: (r) =>
        r.priced ? (
          formatMoney(r.unit_price, undefined, { digits: 8 })
        ) : (
          <button className="link small" onClick={() => openAdd({ sku: r.sku, unit: r.unit })}>
            Add rate
          </button>
        ),
    },
  ]

  if (book.error && !b) return <Notice kind="bad">{book.error}</Notice>
  if (!b) return <Skeleton lines={6} />

  const cov = coverage.data
  const assigned = cov?.customers ?? []
  const money = (v: number | string | null | undefined, digits?: number) => formatMoney(v, currency, digits === undefined ? undefined : { digits })

  return (
    <div className="stack">
      <PageHeader
        crumbs={[{ to: '/pricebooks', label: 'Price books' }, { label: b.name }]}
        title={b.name}
        sub={
          <>
            {b.currency} · annual ÷ {b.annual_divisor.toLocaleString()} h · stopped compute {billStoppedLabel(b.bill_stopped)} · effective {b.effective_from ? day(b.effective_from) : 'always'} ·{' '}
            {items.length} item{items.length === 1 ? '' : 's'} ·{' '}
            {assigned.length ? (
              <span title={assigned.map((c) => c.name).join(', ')}>
                {assigned.length} customer{assigned.length === 1 ? '' : 's'}
              </span>
            ) : (
              'no customer assigned'
            )}
          </>
        }
        actions={
          <>
            <button onClick={() => setDialog({ kind: 'settings' })}>Edit settings</button>
            <button onClick={() => setDialog({ kind: 'import' })}>Import CSV</button>
            <a href={`${API_BASE}/pricebooks/${id}/export.csv`}>
              <button>Export CSV</button>
            </a>
            <button onClick={() => setDialog({ kind: 'clone' })}>Clone</button>
            <button className="danger" onClick={() => setDialog({ kind: 'delete' })}>
              Delete
            </button>
          </>
        }
      />
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {flash ? <Notice kind="ok">{flash}</Notice> : null}

      <div className="kpis">
        <KPI
          label="Coverage"
          value={cov ? formatPct(cov.coverage_pct, { digits: 0 }) : coverage.error ? '—' : '…'}
          note={cov ? (cov.skus_in_use.length ? `of ${cov.skus_in_use.length} SKU${cov.skus_in_use.length === 1 ? '' : 's'} in use, last 30 days` : 'no usage from this book’s customers yet') : (coverage.error ?? 'measuring')}
          tone={cov ? (cov.unpriced_count ? 'warn' : cov.skus_in_use.length ? 'ok' : undefined) : undefined}
        />
        <KPI label="Unpriced SKUs" value={cov ? cov.unpriced_count : '…'} note={cov?.unpriced_count ? 'their usage costs 0 until a rate is added' : 'every SKU in use carries a rate'} tone={cov?.unpriced_count ? 'warn' : undefined} />
        <KPI label="Items" value={items.length} note={`${currency} per unit · annual ÷ ${b.annual_divisor.toLocaleString()}`} />
        <KPI label="Customers" value={assigned.length} note={assigned.length ? assigned.map((c) => c.name).join(', ') : 'assign a customer from its detail page'} />
      </div>

      <div className="card">
        <div className="card-head">
          <h2>Coverage — SKUs in use, last 30 days</h2>
          <span className="hint">from the assigned customers’ collected usage · unpriced first</span>
        </div>
        {coverage.error ? (
          <Notice kind="bad">{coverage.error}</Notice>
        ) : !cov ? (
          <Skeleton lines={3} />
        ) : (
          <DataTable
            columns={covCols}
            rows={covRows}
            rowKey={(r) => r.sku}
            defaultSort={{ key: 'priced', dir: 'asc' }}
            pageSize={12}
            emptyTitle="No usage to cover"
            emptyBody={assigned.length ? 'The assigned customers have no collected usage in the last 30 days — their cost sources have not reported yet.' : 'No customer is on this book, so there is no usage to price against it.'}
          />
        )}
      </div>

      <div className="card">
        <div className="card-head">
          <h2>Items</h2>
          <span className="hint">edit a row and save it · unit price = annual ÷ {b.annual_divisor.toLocaleString()}</span>
        </div>
        <div className="row between" style={{ marginBottom: 10 }}>
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search SKU or description" aria-label="Search items" style={{ maxWidth: 320 }} />
          <div className="row">
            <a href={dataUrl(PRICE_CSV_SAMPLE)} download="pricebook-sample.csv" className="small">
              sample CSV
            </a>
            <button className="primary" onClick={() => openAdd()}>
              Add item
            </button>
          </div>
        </div>

        {add ? (
          <form ref={addRef} className="card flat stack tight" onSubmit={submitAdd} style={{ marginBottom: 10, background: 'var(--panel-2)' }}>
            <div className="row between">
              <h3 style={{ margin: 0 }}>New item</h3>
              <span className="muted small">unit price follows the annual price; edit either</span>
            </div>
            <div className="grid3">
              <Field label="SKU">
                <input className="mono" value={add.sku} onChange={(e) => setAdd({ ...add, sku: e.target.value })} placeholder="ecs.s6.large.2" autoFocus={!add.sku} />
              </Field>
              <Field label="Unit">
                <input value={add.unit} onChange={(e) => setAdd({ ...add, unit: e.target.value })} placeholder="instance-hour" />
              </Field>
              <Field label="Description">
                <input value={add.description} onChange={(e) => setAdd({ ...add, description: e.target.value })} />
              </Field>
              <Field label={`Annual price (${currency}/yr)`}>
                <input type="number" step="any" min={0} value={add.annual_price} onChange={(e) => setAdd(linked(add, { annual_price: e.target.value }, divisor) as AddDraft)} autoFocus={Boolean(add.sku)} />
              </Field>
              <Field label={`Unit price (${currency}/unit)`} help={add.annual_price ? `${add.annual_price} ÷ ${divisor.toLocaleString()}` : undefined}>
                <input type="number" step="any" min={0} value={add.unit_price} onChange={(e) => setAdd(linked(add, { unit_price: e.target.value }, divisor) as AddDraft)} />
              </Field>
            </div>
            {addErr ? <Notice kind="bad">{addErr}</Notice> : null}
            <div className="row end">
              <button type="button" onClick={() => setAdd(null)} disabled={addBusy}>
                Cancel
              </button>
              <button className="primary" disabled={addBusy}>
                Add item
              </button>
            </div>
          </form>
        ) : null}

        {items.length === 0 ? (
          <EmptyState title="No items">This book prices nothing yet, so every SKU its customers use costs 0. Add an item, or import the list as CSV.</EmptyState>
        ) : visible.length === 0 ? (
          <EmptyState title={`No item matches “${q}”`}>Search matches the SKU and the description.</EmptyState>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  {sortCols.map((c) => (
                    <th
                      key={c.key}
                      className={`${c.numeric ? 'num' : ''} sortable ${sort.key === c.key ? 'sorted' : ''}`}
                      onClick={() => toggleSort(c)}
                      aria-sort={sort.key === c.key ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'}
                    >
                      {c.header}
                      <span className="sort">{sort.key === c.key ? (sort.dir === 'asc' ? '↑' : '↓') : '↕'}</span>
                    </th>
                  ))}
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {visible.map((it) => {
                  const base = draftOf(it, divisor)
                  const d = edits[it.sku] ?? base
                  const dirty = edits[it.sku] !== undefined && !sameDraft(d, base)
                  const busy = busySku === it.sku
                  return (
                    <tr key={it.sku}>
                      <td className="mono nowrap">{it.sku}</td>
                      <td>
                        <input value={d.description} onChange={(e) => setDraft(it.sku, { description: e.target.value })} aria-label={`${it.sku} description`} />
                      </td>
                      <td style={{ width: 130 }}>
                        <input value={d.unit} onChange={(e) => setDraft(it.sku, { unit: e.target.value })} aria-label={`${it.sku} unit`} />
                      </td>
                      <td className="num" style={{ width: 150 }}>
                        <input type="number" step="any" min={0} value={d.annual_price} onChange={(e) => setDraft(it.sku, { annual_price: e.target.value })} aria-label={`${it.sku} annual price`} />
                      </td>
                      <td className="num" style={{ width: 170 }}>
                        <input type="number" step="any" min={0} value={d.unit_price} onChange={(e) => setDraft(it.sku, { unit_price: e.target.value })} aria-label={`${it.sku} unit price`} />
                        {!dirty ? <span className="sub">{money(it.unit_price, 8)}</span> : null}
                        {rowErr[it.sku] ? <span className="sub bad">{rowErr[it.sku]}</span> : null}
                      </td>
                      <td className="nowrap">
                        <span className="btn-row">
                          <button className="primary small" disabled={!dirty || busy} onClick={() => void saveRow(it)}>
                            Save
                          </button>
                          {dirty ? (
                            <button className="link small" disabled={busy} onClick={() => revert(it.sku)}>
                              Revert
                            </button>
                          ) : null}
                          <button className="link small danger" disabled={busy} onClick={() => setDialog({ kind: 'delete-item', item: it })}>
                            Delete
                          </button>
                        </span>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
            <div className="table-foot">
              <span>
                {visible.length.toLocaleString()} of {items.length.toLocaleString()} item{items.length === 1 ? '' : 's'}
                {Object.keys(edits).length ? ` · ${Object.keys(edits).length} unsaved` : ''}
              </span>
              <span>
                {num(items.reduce((n, it) => n + annualOf(it, divisor), 0), 3)} {currency}/yr listed in total
              </span>
            </div>
          </div>
        )}
      </div>

      {dialog?.kind === 'settings' ? (
        <BookSettingsModal
          title="Price book settings"
          initial={settingsFrom(b)}
          submitLabel="Save"
          onClose={() => setDialog(null)}
          onSubmit={async (body) => {
            await api.put(`/pricebooks/${id}`, body)
            setDialog(null)
            ok('settings saved')
            await reload()
          }}
        />
      ) : null}
      {dialog?.kind === 'import' ? (
        <ImportModal
          book={b}
          onClose={() => setDialog(null)}
          onImported={(n, mode) => {
            setDialog(null)
            ok(`${n} row${n === 1 ? '' : 's'} imported (${mode === 'replace' ? 'list replaced' : 'merged'})`)
            void reload()
          }}
        />
      ) : null}
      {dialog?.kind === 'clone' ? (
        <CloneBookModal
          book={b}
          onClose={() => setDialog(null)}
          onCloned={(copy) => {
            setDialog(null)
            if (copy) nav(`/pricebooks/${copy.id}`)
            else {
              ok(`${b.name} cloned`)
              nav('/pricebooks')
            }
          }}
        />
      ) : null}
      {dialog?.kind === 'delete' ? <DeleteBookConfirm book={b} assigned={assigned.length} onClose={() => setDialog(null)} onDeleted={() => nav('/pricebooks')} /> : null}
      {dialog?.kind === 'delete-item' ? (
        <Confirm
          title={`Remove ${dialog.item.sku}?`}
          danger
          confirmLabel="Remove item"
          busy={busySku === dialog.item.sku}
          onClose={() => setDialog(null)}
          onConfirm={() => deleteRow(dialog.item)}
          body={
            <p>
              Usage of <span className="mono">{dialog.item.sku}</span> by this book’s customers will rate at 0 from the next run. Issued statements are not changed.
            </p>
          }
        />
      ) : null}
    </div>
  )
}

/** Import CSV: file or pasted text, previewed with lib/csv before anything is sent. */
function ImportModal({ book, onClose, onImported }: { book: PriceBook; onClose: () => void; onImported: (rows: number, mode: 'merge' | 'replace') => void }) {
  const [mode, setMode] = useState<'merge' | 'replace'>('merge')
  const [csv, setCsv] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const preview = useMemo(() => (csv.trim() ? parsePriceBookCsv(csv) : null), [csv])
  const divisor = book.annual_divisor > 0 ? book.annual_divisor : 8760

  useEffect(() => {
    if (!file) return
    void file.text().then((t) => setCsv(t))
  }, [file])

  const run = async () => {
    if (!preview || preview.rows.length === 0) return
    setBusy(true)
    setError('')
    try {
      const form = new FormData()
      form.append('file', file ?? new Blob([csv], { type: 'text/csv' }), file?.name ?? 'pricebook.csv')
      await api.upload(`/pricebooks/${book.id}/import${mode === 'merge' ? '?merge=true' : ''}`, form)
      onImported(preview.rows.length, mode)
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={`Import items into ${book.name}`}
      wide
      onClose={onClose}
      footer={
        <>
          <button onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" disabled={busy || !preview || preview.rows.length === 0} onClick={() => void run()}>
            Import {preview?.rows.length ?? 0} row{preview?.rows.length === 1 ? '' : 's'}
          </button>
        </>
      }
    >
      <div className="stack tight">
        <p className="muted small" style={{ margin: 0 }}>
          Columns <code>sku,unit,annual_price,description</code>; the unit price is annual_price ÷ {divisor.toLocaleString()}.{' '}
          <a href={`${API_BASE}/pricebooks/template.csv`}>Template</a> · <a href={dataUrl(PRICE_CSV_SAMPLE)} download="pricebook-sample.csv">sample</a>
        </p>
        <div className="row">
          <label className="check">
            <input type="radio" name="mode" checked={mode === 'merge'} onChange={() => setMode('merge')} /> Merge — update matching SKUs, add new ones, keep the rest
          </label>
          <label className="check">
            <input type="radio" name="mode" checked={mode === 'replace'} onChange={() => setMode('replace')} /> Replace — the file becomes the whole list
          </label>
        </div>
        {mode === 'replace' && preview ? <Notice kind="warn">Every item not in this file is removed from the book.</Notice> : null}
        <input
          type="file"
          accept=".csv,text/csv"
          onChange={(e) => {
            setFile(e.target.files?.[0] ?? null)
          }}
        />
        <textarea
          value={csv}
          onChange={(e) => {
            setCsv(e.target.value)
            setFile(null)
          }}
          placeholder="…or paste CSV here"
        />
        {error ? <Notice kind="bad">{error}</Notice> : null}
        {preview ? (
          <>
            {preview.errors.length ? (
              <Notice kind="warn">
                {preview.errors.length} line{preview.errors.length === 1 ? '' : 's'} will be skipped:
                <ul style={{ margin: '4px 0 0 18px' }}>
                  {preview.errors.slice(0, 8).map((e) => (
                    <li key={e.line}>
                      line {e.line}: {e.message}
                    </li>
                  ))}
                  {preview.errors.length > 8 ? <li>… {preview.errors.length - 8} more</li> : null}
                </ul>
              </Notice>
            ) : null}
            {preview.rows.length ? (
              <div className="table-wrap" style={{ maxHeight: 280, overflowY: 'auto' }}>
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
                        <td className="num">{num(unitPrice(r.annual_price, divisor), 8)}</td>
                        <td>{r.description}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <EmptyState title="No valid rows">Every line has a problem listed above; fix the file and paste it again.</EmptyState>
            )}
          </>
        ) : null}
      </div>
    </Modal>
  )
}
