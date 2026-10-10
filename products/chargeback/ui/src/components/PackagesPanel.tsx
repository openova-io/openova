import { useMemo, useState, type FormEvent } from 'react'
import { api, asList, errorText } from '../api/client'
import type { EntitlementState, Feature, PackageCell, PackageCellWrite, PackagesDoc, PriceBook } from '../api/types'
import { STATES, cellOf, cellText, includedFromText, matrixRows, type MatrixRow } from '../lib/packages'
import { useQuery } from '../lib/useQuery'
import { Confirm, EmptyState, Field, FormRow, Modal, Notice, Segmented, Skeleton } from './ui'

/**
 * The Packages tab of a price book (DESIGN.md §22): the book's plan items as
 * columns, the features as rows, every cell a chip — Included, Optional with
 * its add-on price, Not offered — that opens a modal to set the state, the
 * included quantity and the add-on price. "Add feature" opens the feature
 * modal; every row has Edit and Delete. The matrix the tab shows is the very
 * document the storefront publishes (GET /pricebooks/{id}/packages ==
 * GET /public/packages), so what the operator sees is what a prospect sees.
 */
export function PackagesPanel({ book, canManage }: { book: PriceBook; canManage: boolean }) {
  const doc = useQuery<PackagesDoc>(`/pricebooks/${book.id}/packages`)
  const feats = useQuery<unknown>('/features')
  const features = useMemo(() => asList<Feature>(feats.data, 'features'), [feats.data])
  const rows = useMemo(() => matrixRows(doc.data, features), [doc.data, features])
  const [dialog, setDialog] = useState<{ kind: 'cell'; row: MatrixRow; planSku: string } | { kind: 'feature'; feature: Feature | null } | { kind: 'delete'; feature: Feature } | null>(null)
  const [flash, setFlash] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const currency = doc.data?.currency ?? book.currency
  const plans = doc.data?.packages ?? []

  const reload = async () => {
    await Promise.all([doc.reload(), feats.reload()])
  }
  const done = async (msg: string) => {
    setDialog(null)
    setError('')
    setFlash(msg)
    await reload()
  }
  const deleteFeature = async (f: Feature) => {
    setBusy(true)
    try {
      await api.del(`/features/${encodeURIComponent(f.id)}`)
      await done(`${f.name} removed`)
    } catch (e) {
      setDialog(null)
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }

  if (doc.error && !doc.data) return <Notice kind="bad">{doc.error}</Notice>
  if (!doc.data || !feats.data) return <Skeleton lines={4} />

  return (
    <div className="stack" data-testid="packages-panel">
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {flash ? <Notice kind="ok">{flash}</Notice> : null}
      <div className="card">
        <div className="card-head">
          <h2>Packages</h2>
          <span className="hint">
            {plans.length} package{plans.length === 1 ? '' : 's'} · {features.length} feature{features.length === 1 ? '' : 's'} · published as of {doc.data.prices_as_of}
          </span>
        </div>
        <div className="row between" style={{ marginBottom: 10 }}>
          <p className="muted small" style={{ margin: 0 }}>
            A feature is <b>Included</b> (on the invoice at 0.000), <b>Optional</b> (a paid add-on, taken per Organization) or <b>Not offered</b>. A quantity feature that is included carries how much — the engine applies it as an allowance. Click a cell to change it. This is the matrix the storefront and the calculator show.
          </p>
          {canManage ? (
            <button className="primary" onClick={() => setDialog({ kind: 'feature', feature: null })}>
              Add feature
            </button>
          ) : null}
        </div>
        {plans.length === 0 ? (
          <EmptyState title="No packages in this book">A package is a priced plan item — plan.s, plan.m, plan.l, plan.xl. This book prices none, so there is nothing to put a feature on. The “OpenOva plans” book carries them.</EmptyState>
        ) : rows.length === 0 ? (
          <EmptyState title="No features yet">Add the features the marketplace lists — SSL, backup, SSO, bandwidth — and say, per package, whether each is included, optional or not offered.</EmptyState>
        ) : (
          <div className="table-wrap">
            <table className="pkg-table" aria-label="Package matrix">
              <thead>
                <tr>
                  <th>Feature</th>
                  {plans.map((p) => (
                    <th key={p.sku} className="pkg-head">
                      <div>{p.name}</div>
                      <div className="small muted num">
                        {p.price_month} {currency} / month
                      </div>
                    </th>
                  ))}
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.feature.id} data-testid={`feature-row-${r.feature.key}`}>
                    <td>
                      <div>
                        <b>{r.feature.name}</b> <span className="mono muted tiny">{r.feature.key}</span>
                      </div>
                      {r.feature.blurb ? <div className="small muted">{r.feature.blurb}</div> : null}
                      <div className="tiny muted">
                        {r.feature.kind === 'quantity' ? `quantity · ${r.feature.unit ?? ''}` : 'boolean'}
                        {r.feature.addon_sku ? (
                          <>
                            {' · '}
                            <span className="mono">{r.feature.addon_sku}</span>
                          </>
                        ) : null}
                        {!r.inBook ? ' · not in this book yet' : ''}
                      </div>
                    </td>
                    {plans.map((p) => {
                      const cell = cellOf({ cells: r.cells }, p.sku)
                      const hint = cell.state !== 'included' ? includedFromText(doc.data, cell) : ''
                      return (
                        <td key={p.sku} className="pkg-cell-td">
                          <button
                            type="button"
                            className={`pkg-cell ${cell.state}`}
                            disabled={!canManage}
                            aria-label={`${r.feature.name} on ${p.name}`}
                            onClick={() => setDialog({ kind: 'cell', row: r, planSku: p.sku })}
                            data-testid={`cell-${r.feature.key}-${p.sku}`}
                          >
                            <span>{cellText(r.feature, cell, currency)}</span>
                            {hint ? <span className="tiny muted">{hint}</span> : null}
                            {cell.note ? <span className="tiny muted">{cell.note}</span> : null}
                          </button>
                        </td>
                      )
                    })}
                    <td className="nowrap">
                      {canManage ? (
                        <span className="btn-row">
                          <button className="link small" onClick={() => setDialog({ kind: 'feature', feature: r.feature })}>
                            Edit
                          </button>
                          <button className="link small danger" onClick={() => setDialog({ kind: 'delete', feature: r.feature })}>
                            Delete
                          </button>
                        </span>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {dialog?.kind === 'cell' ? (
        <CellModal
          book={book}
          doc={doc.data}
          row={dialog.row}
          planSku={dialog.planSku}
          currency={currency}
          onClose={() => setDialog(null)}
          onSaved={(what) => done(what)}
        />
      ) : null}
      {dialog?.kind === 'feature' ? <FeatureModal feature={dialog.feature} onClose={() => setDialog(null)} onSaved={(f) => done(`${f.name} saved`)} /> : null}
      {dialog?.kind === 'delete' ? (
        <Confirm
          title={`Delete ${dialog.feature.name}?`}
          danger
          confirmLabel="Delete feature"
          busy={busy}
          onClose={() => setDialog(null)}
          onConfirm={() => deleteFeature(dialog.feature)}
          body={
            <p>
              <span className="mono">{dialog.feature.key}</span> leaves the matrix. It is refused while any package of any book carries a cell for it, or any Organization has taken it as an add-on — clear those first.
            </p>
          }
        />
      ) : null}
    </div>
  )
}

/** One cell: the state, the included quantity of a quantity feature, the add-on price per month of a boolean one, and a note. */
export function CellModal({
  book,
  doc,
  row,
  planSku,
  currency,
  onClose,
  onSaved,
}: {
  book: PriceBook
  doc: PackagesDoc
  row: MatrixRow
  planSku: string
  currency: string
  onClose: () => void
  onSaved: (what: string) => void | Promise<void>
}) {
  const feature = row.feature
  const cell: PackageCell = cellOf({ cells: row.cells }, planSku)
  const plan = doc.packages.find((p) => p.sku === planSku)
  const [state, setState] = useState<EntitlementState>((cell.state as EntitlementState) || 'not_offered')
  const [quantity, setQuantity] = useState(cell.quantity === undefined || cell.quantity === null ? '' : String(cell.quantity))
  const [price, setPrice] = useState(cell.price_month ?? '')
  const [note, setNote] = useState(cell.note ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const isQuantity = feature.kind === 'quantity'

  const problem = (() => {
    if (state === 'included' && isQuantity && (quantity.trim() === '' || !Number.isFinite(Number(quantity)) || Number(quantity) < 0)) return `say how much ${feature.name} the ${plan?.name ?? planSku} package includes (${feature.unit ?? ''})`
    if (state === 'optional' && !feature.addon_sku) return `${feature.name} has no add-on SKU; set one on the feature before offering it as an add-on`
    if (state === 'optional' && !isQuantity && price.trim() !== '' && (!Number.isFinite(Number(price)) || Number(price) < 0)) return 'the add-on price must be a non-negative number'
    return ''
  })()

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (problem) return
    const body: PackageCellWrite = { state, note }
    if (isQuantity && quantity.trim() !== '') body.included_quantity = quantity.trim()
    if (state === 'optional' && !isQuantity && price.trim() !== '') body.addon_monthly = price.trim()
    setBusy(true)
    setError('')
    try {
      await api.put(`/pricebooks/${book.id}/packages/${encodeURIComponent(planSku)}/features/${encodeURIComponent(feature.key)}`, body)
      await onSaved(`${feature.name} on ${plan?.name ?? planSku}: ${STATES.find((s) => s.value === state)?.label.toLowerCase()}`)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }
  const remove = async () => {
    setBusy(true)
    setError('')
    try {
      await api.del(`/pricebooks/${book.id}/packages/${encodeURIComponent(planSku)}/features/${encodeURIComponent(feature.key)}`)
      await onSaved(`${feature.name} removed from ${plan?.name ?? planSku}`)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={`${feature.name} on ${plan?.name ?? planSku}`}
      onClose={onClose}
      footer={
        <>
          {row.inBook ? (
            <button type="button" className="link small danger" disabled={busy} onClick={() => void remove()} title="Remove the cell; the package then reads not offered">
              Remove cell
            </button>
          ) : null}
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="package-cell-form" disabled={busy || Boolean(problem)}>
            Save
          </button>
        </>
      }
    >
      <form id="package-cell-form" onSubmit={(e) => void submit(e)} className="stack tight">
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <div className="field">
          <label id="package-state-label">State</label>
          <Segmented value={state} options={STATES.map((s) => ({ value: s.value, label: s.label, title: s.help }))} onChange={setState} ariaLabel="State" />
          <div className="help">{STATES.find((s) => s.value === state)?.help}</div>
        </div>
        <FormRow>
          {isQuantity ? (
            <Field label={`Included quantity (${feature.unit ?? ''})`} help={state === 'included' ? `What the ${plan?.name ?? planSku} package includes every plan-hour; the excess is billed at ${feature.addon_sku ?? 'the feature’s SKU'}.` : 'Kept as information on a cell that is not included.'}>
              <input type="number" min={0} step="any" inputMode="decimal" value={quantity} onChange={(e) => setQuantity(e.target.value)} />
            </Field>
          ) : state === 'optional' ? (
            <Field label={`Add-on price (${currency} / month)`} help={feature.addon_sku ? `Prices ${feature.addon_sku} in ${book.name} per plan-hour, like the plan: monthly × 12 ÷ ${book.annual_divisor.toLocaleString()}.` : undefined}>
              <input type="number" min={0} step="any" inputMode="decimal" value={price} onChange={(e) => setPrice(e.target.value)} placeholder={cell.price_month ? undefined : 'e.g. 1.500'} />
            </Field>
          ) : null}
          <Field label="Note" help="Shown under the cell and published with it (retention, limits).">
            <input value={note} onChange={(e) => setNote(e.target.value)} />
          </Field>
        </FormRow>
        {problem ? <div className="err small">{problem}</div> : null}
      </form>
    </Modal>
  )
}

/** Create or edit a feature. The key is set once: the matrix, the add-ons and the invoices name it. */
export function FeatureModal({ feature, onClose, onSaved }: { feature: Feature | null; onClose: () => void; onSaved: (f: Feature) => void | Promise<void> }) {
  const [key, setKey] = useState(feature?.key ?? '')
  const [name, setName] = useState(feature?.name ?? '')
  const [blurb, setBlurb] = useState(feature?.blurb ?? '')
  const [kind, setKind] = useState<'boolean' | 'quantity'>(feature?.kind === 'quantity' ? 'quantity' : 'boolean')
  const [unit, setUnit] = useState(feature?.unit ?? '')
  const [addonSku, setAddonSku] = useState(feature?.addon_sku ?? '')
  const [sortOrder, setSortOrder] = useState(String(feature?.sort_order ?? 0))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const problem = (() => {
    if (!feature && !/^[a-z0-9][a-z0-9_.-]*$/.test(key.trim())) return 'the key is lower-case letters, digits, dot, dash or underscore'
    if (!name.trim()) return 'a feature needs a name'
    if (kind === 'quantity' && !unit.trim()) return 'a quantity feature needs a unit (Mbps, GB)'
    if (sortOrder.trim() !== '' && !Number.isInteger(Number(sortOrder))) return 'the sort order is a whole number'
    return ''
  })()

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (problem) return
    setBusy(true)
    setError('')
    try {
      const body = { name: name.trim(), blurb: blurb.trim(), kind, unit: kind === 'quantity' ? unit.trim() : '', addon_sku: addonSku.trim(), sort_order: Number(sortOrder || 0) }
      const saved = feature ? await api.patch<Feature>(`/features/${encodeURIComponent(feature.id)}`, body) : await api.post<Feature>('/features', { key: key.trim(), ...body })
      await onSaved(saved)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={feature ? `Edit ${feature.name}` : 'Add feature'}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="feature-form" disabled={busy || Boolean(problem)}>
            {feature ? 'Save' : 'Add feature'}
          </button>
        </>
      }
    >
      <form id="feature-form" onSubmit={(e) => void submit(e)} className="stack tight">
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <FormRow>
          <Field label="Key" help={feature ? 'Set once; the matrix, the add-ons and the invoices name it.' : 'backup, ssl, bandwidth — lower-case, stable.'}>
            <input className="mono" value={key} onChange={(e) => setKey(e.target.value)} disabled={Boolean(feature)} autoFocus={!feature} />
          </Field>
          <Field label="Name">
            <input value={name} onChange={(e) => setName(e.target.value)} autoFocus={Boolean(feature)} />
          </Field>
        </FormRow>
        <Field label="Blurb" help="One line a prospect reads under the name.">
          <input value={blurb} onChange={(e) => setBlurb(e.target.value)} />
        </Field>
        <FormRow>
          <Field label="Kind" help={kind === 'quantity' ? 'Comes with a quantity the package includes; the engine applies it as an allowance.' : 'The package carries it or not.'}>
            <select value={kind} onChange={(e) => setKind(e.target.value as 'boolean' | 'quantity')}>
              <option value="boolean">Boolean</option>
              <option value="quantity">Quantity</option>
            </select>
          </Field>
          {kind === 'quantity' ? (
            <Field label="Unit">
              <input value={unit} onChange={(e) => setUnit(e.target.value)} placeholder="Mbps" />
            </Field>
          ) : null}
          <Field label="Add-on SKU" help={kind === 'quantity' ? 'The metered SKU the included quantity is an allowance on.' : 'The SKU billed when a package offers the feature as an add-on.'}>
            <input className="mono" value={addonSku} onChange={(e) => setAddonSku(e.target.value)} placeholder={kind === 'quantity' ? 'eip.bandwidth_mbps' : 'addon.backup'} />
          </Field>
          <Field label="Sort order">
            <input type="number" step={1} inputMode="numeric" value={sortOrder} onChange={(e) => setSortOrder(e.target.value)} />
          </Field>
        </FormRow>
        {problem ? <div className="err small">{problem}</div> : null}
      </form>
    </Modal>
  )
}
