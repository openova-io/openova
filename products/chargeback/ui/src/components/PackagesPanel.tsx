import { useMemo, useState, type FormEvent } from 'react'
import { api, asList, errorText } from '../api/client'
import type { EntitlementState, Feature, FeatureGroup, FeatureKind, Overage, PackageCell, PackageCellWrite, PackageInfo, PackageSettingsWrite, PackagesDoc, PriceBook } from '../api/types'
import { BADGE_MAX, badgeProblem, iconIdOf, iconRefOf, isColour, normalizeColour } from '../lib/icons'
import { DEFAULT_GROUPS, KINDS, OVERAGES, STATES, cellOf, cellSubText, cellText, floorRows, groupedRows, includedFromText, shapeGuaranteed, shapeHeadline, stepUpText, type MatrixRow } from '../lib/packages'
import { useQuery } from '../lib/useQuery'
import { ColourField, IconField, PackageIcon } from './Icons'
import { Confirm, EmptyState, Field, FormRow, Modal, Notice, Segmented, Skeleton } from './ui'

/**
 * The Packages tab of a price book (DESIGN.md §22.5): the book's plan items
 * as columns — price, tagline, shape, the Recommended badge and a Settings
 * button each — a STEP-UP CHECK row under the header, then the features as
 * rows grouped under their group heading, every cell a chip in its kind's
 * words (Included · + 1.500 OMR / month · 50 Mbps hard cap · a level's label
 * · from XL · Not offered) that opens the cell modal for that kind. "Add
 * feature" opens the feature modal (group, kind, levels, teaser); every row
 * has Edit and Delete. The floor items — on every package, never a cell —
 * are a strip under the matrix, each with Edit and Delete. The matrix the
 * tab shows is the very document the storefront publishes
 * (GET /pricebooks/{id}/packages == GET /public/packages), so what the
 * operator sees is what a prospect sees. No form sits on the page; every
 * write is a modal from the row, the cell or the column (#6946).
 */
export function PackagesPanel({ book, canManage }: { book: PriceBook; canManage: boolean }) {
  const doc = useQuery<PackagesDoc>(`/pricebooks/${book.id}/packages`)
  const feats = useQuery<unknown>('/features')
  const features = useMemo(() => asList<Feature>(feats.data, 'features'), [feats.data])
  const groups = useMemo(() => groupedRows(doc.data, features), [doc.data, features])
  const floor = useMemo(() => floorRows(features), [features])
  const [dialog, setDialog] = useState<{ kind: 'cell'; row: MatrixRow; planSku: string } | { kind: 'feature'; feature: Feature | null; floor?: boolean } | { kind: 'delete'; feature: Feature } | { kind: 'settings'; pkg: PackageInfo } | { kind: 'group'; group: FeatureGroup } | null>(null)
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
  const rowCount = groups.reduce((n, g) => n + g.rows.length, 0)

  return (
    <div className="stack" data-testid="packages-panel">
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {flash ? <Notice kind="ok">{flash}</Notice> : null}
      <div className="card">
        <div className="card-head">
          <h2>Packages</h2>
          <span className="hint">
            {plans.length} package{plans.length === 1 ? '' : 's'} · {rowCount} feature{rowCount === 1 ? '' : 's'} · {floor.length} on the floor · published as of {doc.data.prices_as_of}
          </span>
        </div>
        <div className="row between" style={{ marginBottom: 10 }}>
          <p className="muted small" style={{ margin: 0 }}>
            A package is a position on each group. A boolean feature is <b>Included</b>, <b>Optional</b> (a paid add-on) or <b>Not offered</b>; a quantity feature includes how much and says what happens above it; a level feature is at one of its levels, the next one purchasable or not; an access door is open or not. The floor is on every package. Click a cell to change it; the column header opens the package’s settings. This is the matrix the storefront and the calculator show.
          </p>
          {canManage ? (
            <span className="btn-row">
              <button onClick={() => setDialog({ kind: 'feature', feature: null, floor: true })}>Add floor item</button>
              <button className="primary" onClick={() => setDialog({ kind: 'feature', feature: null })}>
                Add feature
              </button>
            </span>
          ) : null}
        </div>
        {plans.length === 0 ? (
          <EmptyState title="No packages in this book">A package is a priced plan item — plan.s, plan.m, plan.l, plan.xl. This book prices none, so there is nothing to put a feature on. The “OpenOva plans” book carries them.</EmptyState>
        ) : rowCount === 0 ? (
          <EmptyState title="No features yet">Add the features the marketplace lists — bandwidth, backups, the platform doors — and say, per package, where each one stands.</EmptyState>
        ) : (
          <div className="table-wrap">
            <table className="pkg-table" aria-label="Package matrix">
              <thead>
                <tr>
                  <th>Feature</th>
                  {plans.map((p) => {
                    const guaranteed = shapeGuaranteed(p)
                    return (
                      <th key={p.sku} className={`pkg-head${p.accent ? ' accented' : ''}`} style={p.accent ? { borderTopColor: p.accent } : undefined} data-testid={`pkg-head-${p.sku}`}>
                        {p.badge ? (
                          <div className="pkg-badge" style={p.accent ? { background: p.accent } : undefined} data-testid={`pkg-badge-${p.sku}`}>
                            {p.badge}
                          </div>
                        ) : null}
                        <div className="pkg-name">
                          <PackageIcon icon={p.icon} size={18} />
                          {p.name}
                          {p.recommended ? <span className="badge ok pkg-recommended">Recommended</span> : null}
                        </div>
                        {p.tagline ? <div className="small muted pkg-tagline">{p.tagline}</div> : null}
                        <div className="small muted num">
                          {p.price_month} {currency} / month
                        </div>
                        <div className="tiny pkg-shape">{shapeHeadline(p) || '—'}</div>
                        {guaranteed ? <div className="tiny muted pkg-guaranteed">{guaranteed}</div> : null}
                        {canManage ? (
                          <button type="button" className="link small" onClick={() => setDialog({ kind: 'settings', pkg: p })} aria-label={`Settings of ${p.name}`}>
                            Settings
                          </button>
                        ) : null}
                      </th>
                    )
                  })}
                  <th></th>
                </tr>
                <tr className="pkg-stepup" data-testid="step-up-row">
                  <th className="muted small">Step-up check</th>
                  {plans.map((p) => {
                    const su = stepUpText(p, currency)
                    return (
                      <th key={p.sku} className={`small num ${su ? (su.ok ? 'ok' : 'bad') : 'muted'}`} data-testid={`step-up-${p.sku}`} title={su ? `The add-ons ${p.name} offers that ${p.step_up?.next_name} includes must be worth at least the step to it` : undefined}>
                        {su ? su.text : 'top package'}
                      </th>
                    )
                  })}
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {groups.map((g) => (
                  <GroupRows key={g.group.key} group={g} plans={plans} doc={doc.data!} currency={currency} canManage={canManage} onCell={(row, planSku) => setDialog({ kind: 'cell', row, planSku })} onEdit={(f) => setDialog({ kind: 'feature', feature: f })} onDelete={(f) => setDialog({ kind: 'delete', feature: f })} onGroupIcon={(group) => setDialog({ kind: 'group', group })} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="card" data-testid="floor-strip">
        <div className="card-head">
          <h2>Floor items</h2>
          <span className="hint">on every package · never priced · no cell</span>
        </div>
        {floor.length === 0 ? (
          <EmptyState title="Nothing on the floor">A floor item is listed once under every package — SSL, SSO, DDoS protection. Add one with “Add floor item”.</EmptyState>
        ) : (
          <div className="pkg-floor">
            {floor.map((f) => (
              <span key={f.id} className="pkg-floor-item" data-testid={`floor-${f.key}`}>
                <PackageIcon icon={iconRefOf(f.icon_id, f.name, f.icon_bg)} size={16} />
                <b>{f.name}</b>
                {f.blurb ? <span className="muted small"> — {f.blurb}</span> : null}
                {canManage ? (
                  <span className="btn-row">
                    <button className="link small" onClick={() => setDialog({ kind: 'feature', feature: f })}>
                      Edit
                    </button>
                    <button className="link small danger" onClick={() => setDialog({ kind: 'delete', feature: f })}>
                      Delete
                    </button>
                  </span>
                ) : null}
              </span>
            ))}
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
      {dialog?.kind === 'feature' ? <FeatureModal feature={dialog.feature} floor={dialog.floor} groups={doc.data.groups?.length ? doc.data.groups : DEFAULT_GROUPS} onClose={() => setDialog(null)} onSaved={(f) => done(`${f.name} saved`)} /> : null}
      {dialog?.kind === 'settings' ? <PackageSettingsModal book={book} pkg={dialog.pkg} currency={currency} onClose={() => setDialog(null)} onSaved={() => done(`${dialog.pkg.name} settings saved`)} /> : null}
      {dialog?.kind === 'group' ? <GroupIconModal group={dialog.group} onClose={() => setDialog(null)} onSaved={() => done(`${dialog.group.name} icon saved`)} /> : null}
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

function GroupRows({
  group,
  plans,
  doc,
  currency,
  canManage,
  onCell,
  onEdit,
  onDelete,
  onGroupIcon,
}: {
  group: { group: FeatureGroup; rows: MatrixRow[] }
  plans: PackageInfo[]
  doc: PackagesDoc
  currency: string
  canManage: boolean
  onCell: (row: MatrixRow, planSku: string) => void
  onEdit: (f: Feature) => void
  onDelete: (f: Feature) => void
  onGroupIcon?: (g: FeatureGroup) => void
}) {
  // "other" collects rows whose group the document does not list; it has no
  // settings of its own to carry an icon.
  const editable = canManage && onGroupIcon && group.group.key !== 'other'
  return (
    <>
      <tr className="pkg-group" data-testid={`group-${group.group.key}`}>
        <th colSpan={plans.length + 2}>
          <span className="pkg-group-name">
            <PackageIcon icon={group.group.icon} size={14} />
            {group.group.name}
          </span>
          {editable ? (
            <button type="button" className="link small pkg-group-edit" onClick={() => onGroupIcon(group.group)} aria-label={`Icon of ${group.group.name}`}>
              Icon
            </button>
          ) : null}
        </th>
      </tr>
      {group.rows.map((r) => (
        <tr key={r.feature.id} data-testid={`feature-row-${r.feature.key}`}>
          <td>
            <div className="pkg-feature-name">
              <PackageIcon icon={iconRefOf(r.feature.icon_id, r.feature.name, r.feature.icon_bg)} size={16} />
              <b>{r.feature.name}</b> <span className="mono muted tiny">{r.feature.key}</span>
            </div>
            {r.feature.blurb ? <div className="small muted">{r.feature.blurb}</div> : null}
            <div className="tiny muted">
              {r.feature.kind === 'quantity' ? `quantity · ${r.feature.unit ?? ''}` : r.feature.kind === 'level' ? `level · ${(r.feature.levels ?? []).join(' → ')}` : r.feature.kind}
              {r.feature.teaser ? ' · teaser' : ''}
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
            const hint = cell.state !== 'included' && cell.state !== 'teaser' ? includedFromText(doc, cell) : ''
            const sub = cellSubText(r.feature, cell, currency)
            return (
              <td key={p.sku} className="pkg-cell-td">
                <button
                  type="button"
                  className={`pkg-cell ${cell.state}`}
                  disabled={!canManage}
                  aria-label={`${r.feature.name} on ${p.name}`}
                  onClick={() => onCell(r, p.sku)}
                  data-testid={`cell-${r.feature.key}-${p.sku}`}
                >
                  <span>{cellText(r.feature, cell, currency)}</span>
                  {sub ? <span className="tiny muted">{sub}</span> : null}
                  {hint ? <span className="tiny muted">{hint}</span> : null}
                  {cell.note ? <span className="tiny muted">{cell.note}</span> : null}
                </button>
              </td>
            )
          })}
          <td className="nowrap">
            {canManage ? (
              <span className="btn-row">
                <button className="link small" onClick={() => onEdit(r.feature)}>
                  Edit
                </button>
                <button className="link small danger" onClick={() => onDelete(r.feature)}>
                  Delete
                </button>
              </span>
            ) : null}
          </td>
        </tr>
      ))}
    </>
  )
}

/**
 * One cell, edited in its kind's terms: a boolean's three states and add-on
 * price; a quantity's quantity and overage; a level's level and whether the
 * next is purchasable, at what price; an access door's open or not. A note
 * on every kind, and Remove cell.
 */
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
  const kind = (feature.kind || 'boolean') as FeatureKind
  const levels = feature.levels ?? []
  // A level cell is "at a level" or not offered; whether the next level is
  // purchasable is its own tick, never a third state on the control.
  const stored: EntitlementState = cell.state === 'teaser' || cell.state === 'not_offered' || !cell.state ? 'not_offered' : kind === 'level' ? 'included' : (cell.state as EntitlementState)
  const [state, setState] = useState<EntitlementState>(stored)
  const [quantity, setQuantity] = useState(cell.quantity === undefined || cell.quantity === null ? '' : String(cell.quantity))
  const [overage, setOverage] = useState<Overage>((cell.overage as Overage) || 'metered')
  const [level, setLevel] = useState(cell.level === undefined || cell.level === null ? 0 : cell.level)
  const [nextPurchasable, setNextPurchasable] = useState(Boolean(cell.next_level_addon))
  // DESIGN.md §22.11 — optional only in grow mode, billed as usage: no
  // add-on SKU, no price (active-passive on S, M and L).
  const [growOnly, setGrowOnly] = useState(Boolean(cell.grow_only))
  const [price, setPrice] = useState(cell.price_month ?? cell.next_level_addon?.price_month ?? '')
  const [note, setNote] = useState(cell.note ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  // The states each kind offers.
  const states = kind === 'access' ? STATES.filter((s) => s.value !== 'optional') : kind === 'quantity' ? STATES.filter((s) => s.value !== 'optional') : kind === 'level' ? STATES.filter((s) => s.value !== 'optional') : STATES
  const effectiveState: EntitlementState = kind === 'level' ? (state === 'not_offered' ? 'not_offered' : nextPurchasable || growOnly ? 'optional' : 'included') : state
  const growOnlyOn = growOnly && effectiveState === 'optional' && (kind === 'boolean' || kind === 'level')
  const nextLabel = kind === 'level' ? (levels[level + 1] ?? '') : ''

  const problem = (() => {
    if (kind === 'quantity' && state === 'included' && overage !== 'unlimited' && (quantity.trim() === '' || !Number.isFinite(Number(quantity)) || Number(quantity) < 0)) return `say how much ${feature.name} the ${plan?.name ?? planSku} package includes (${feature.unit ?? ''})`
    if (kind === 'quantity' && state === 'included' && overage === 'metered' && !feature.addon_sku) return `${feature.name} has no SKU to meter above the included quantity; set one on the feature, or cap it`
    if (kind === 'level' && growOnly && !nextLabel) return `${levels[level] ?? 'this'} is the top level; there is nothing above it for grow mode to bring`
    if (kind === 'boolean' && state === 'optional' && !growOnly && !feature.addon_sku) return `${feature.name} has no add-on SKU; set one on the feature before offering it as an add-on`
    if (kind === 'level' && state !== 'not_offered' && (level < 0 || level >= levels.length)) return `choose one of the ${levels.length} levels`
    if (kind === 'level' && nextPurchasable && !nextLabel) return `${levels[level] ?? 'this'} is the top level; there is no next level to offer`
    if (kind === 'level' && nextPurchasable && !feature.addon_sku) return `${feature.name} has no add-on SKU; set one on the feature before offering its next level`
    if ((kind === 'boolean' || kind === 'level') && effectiveState === 'optional' && !growOnlyOn && price.trim() !== '' && (!Number.isFinite(Number(price)) || Number(price) < 0)) return 'the add-on price must be a non-negative number'
    return ''
  })()

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (problem) return
    const body: PackageCellWrite = { state: effectiveState, note }
    if (kind === 'quantity') {
      if (quantity.trim() !== '') body.included_quantity = quantity.trim()
      body.overage = overage
    }
    if (kind === 'level' && state !== 'not_offered') body.level = level
    if ((kind === 'boolean' || kind === 'level') && effectiveState === 'optional' && !growOnlyOn && price.trim() !== '') body.addon_monthly = price.trim()
    if (kind === 'boolean' || kind === 'level') body.grow_only = growOnlyOn
    setBusy(true)
    setError('')
    try {
      await api.put(`/pricebooks/${book.id}/packages/${encodeURIComponent(planSku)}/features/${encodeURIComponent(feature.key)}`, body)
      const said = growOnlyOn
        ? `${kind === 'level' ? `${levels[level]}, ${nextLabel}` : 'optional'} in grow mode only`
        : kind === 'level' && state !== 'not_offered' ? `${levels[level]}${nextPurchasable ? `, ${nextLabel} purchasable` : ''}` : STATES.find((s) => s.value === effectiveState)?.label.toLowerCase()
      await onSaved(`${feature.name} on ${plan?.name ?? planSku}: ${said}`)
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

  const priceField = (label: string, help: string) => (
    <Field label={`${label} (${currency} / month)`} help={help}>
      <input type="number" min={0} step="any" inputMode="decimal" value={price} onChange={(e) => setPrice(e.target.value)} placeholder={price ? undefined : 'e.g. 1.500'} />
    </Field>
  )

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
      <form id="package-cell-form" onSubmit={(e) => void submit(e)} className="stack tight" data-testid={`cell-editor-${kind}`}>
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <div className="field">
          <label id="package-state-label">{kind === 'access' ? 'Door' : kind === 'level' ? 'On this package' : 'State'}</label>
          <Segmented value={state} options={states.map((s) => ({ value: s.value, label: kind === 'access' && s.value === 'included' ? 'Open' : kind === 'level' && s.value === 'included' ? 'At a level' : s.label, title: s.help }))} onChange={setState} ariaLabel="State" />
          <div className="help">{kind === 'access' ? 'A platform door is open or not; it is never priced.' : kind === 'level' && state === 'included' ? 'The package is at one of the feature’s levels; the next one may be purchasable.' : STATES.find((s) => s.value === state)?.help}</div>
        </div>
        {kind === 'quantity' ? (
          <FormRow>
            <Field label={`Included quantity (${feature.unit ?? ''})`} help={state === 'included' ? `What the ${plan?.name ?? planSku} package includes every plan-hour.` : 'Kept as information on a cell that is not included.'}>
              <input type="number" min={0} step="any" inputMode="decimal" value={quantity} onChange={(e) => setQuantity(e.target.value)} aria-label="Included quantity" />
            </Field>
            <Field label="Above it" help={OVERAGES.find((o) => o.value === overage)?.help}>
              <select value={overage} onChange={(e) => setOverage(e.target.value as Overage)} aria-label="Overage">
                {OVERAGES.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </select>
            </Field>
          </FormRow>
        ) : null}
        {kind === 'level' && state !== 'not_offered' ? (
          <>
            <FormRow>
              <Field label="Level" help={`The ${plan?.name ?? planSku} package is at this level.`}>
                <select value={level} onChange={(e) => setLevel(Number(e.target.value))} aria-label="Level">
                  {levels.map((l, i) => (
                    <option key={i} value={i}>
                      {l}
                    </option>
                  ))}
                </select>
              </Field>
              <label className="check">
                <input type="checkbox" checked={nextPurchasable} disabled={!nextLabel || growOnly} onChange={(e) => setNextPurchasable(e.target.checked)} aria-label="Next level purchasable" /> {nextLabel ? `${nextLabel} purchasable as an add-on` : 'top level — nothing above it'}
              </label>
              <label className="check">
                <input type="checkbox" checked={growOnly} disabled={!nextLabel || nextPurchasable} onChange={(e) => setGrowOnly(e.target.checked)} aria-label="Next level in grow mode only" /> {nextLabel ? `${nextLabel} in grow mode only — no upfront price, billed as usage` : 'top level'}
              </label>
            </FormRow>
            {nextPurchasable && !growOnly ? priceField('Next level', feature.addon_sku ? `Prices ${feature.addon_sku} in ${book.name} per plan-hour, like the plan: monthly × 12 ÷ ${book.annual_divisor.toLocaleString()}.` : 'Set an add-on SKU on the feature first.') : null}
          </>
        ) : null}
        {kind === 'boolean' && state === 'optional' ? (
          <label className="check">
            <input type="checkbox" checked={growOnly} onChange={(e) => setGrowOnly(e.target.checked)} aria-label="In grow mode only" /> In grow mode only — no add-on, billed as usage
          </label>
        ) : null}
        {kind === 'boolean' && state === 'optional' && !growOnly ? priceField('Add-on price', feature.addon_sku ? `Prices ${feature.addon_sku} in ${book.name} per plan-hour, like the plan: monthly × 12 ÷ ${book.annual_divisor.toLocaleString()}.` : 'Set an add-on SKU on the feature first.') : null}
        <Field label="Note" help="Shown under the cell and published with it (retention, limits, “read”).">
          <input value={note} onChange={(e) => setNote(e.target.value)} aria-label="Note" />
        </Field>
        {problem ? <div className="err small">{problem}</div> : null}
      </form>
    </Modal>
  )
}

/** Create or edit a feature. The key is set once: the matrix, the add-ons and the invoices name it. */
export function FeatureModal({ feature, floor, groups, onClose, onSaved }: { feature: Feature | null; floor?: boolean; groups: Array<{ key: string; name: string }>; onClose: () => void; onSaved: (f: Feature) => void | Promise<void> }) {
  const [key, setKey] = useState(feature?.key ?? '')
  const [name, setName] = useState(feature?.name ?? '')
  const [blurb, setBlurb] = useState(feature?.blurb ?? '')
  const [group, setGroup] = useState<string>(feature?.group ?? (floor ? 'floor' : 'features'))
  const [kind, setKind] = useState<FeatureKind>((feature?.kind as FeatureKind) ?? 'boolean')
  const [unit, setUnit] = useState(feature?.unit ?? '')
  const [addonSku, setAddonSku] = useState(feature?.addon_sku ?? '')
  const [levelsText, setLevelsText] = useState((feature?.levels ?? []).join('\n'))
  const [teaser, setTeaser] = useState(Boolean(feature?.teaser))
  const [sortOrder, setSortOrder] = useState(String(feature?.sort_order ?? 0))
  const [iconId, setIconId] = useState(feature?.icon_id ?? '')
  const [iconBg, setIconBg] = useState(feature?.icon_bg ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const isFloor = group === 'floor'
  const levels = levelsText
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)

  const problem = (() => {
    if (!feature && !/^[a-z0-9][a-z0-9_.-]*$/.test(key.trim())) return 'the key is lower-case letters, digits, dot, dash or underscore'
    if (!name.trim()) return 'a feature needs a name'
    if (!isFloor && kind === 'quantity' && !unit.trim()) return 'a quantity feature needs a unit (Mbps, GB)'
    if (!isFloor && kind === 'level' && levels.length < 2) return 'a level feature needs at least two levels, one per line, in order'
    if (!isFloor && kind === 'access' && addonSku.trim()) return 'an access door is never priced; it has no add-on SKU'
    if (sortOrder.trim() !== '' && !Number.isInteger(Number(sortOrder))) return 'the sort order is a whole number'
    if (!isColour(iconBg)) return 'the icon background is a colour written #RRGGBB'
    return ''
  })()

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (problem) return
    setBusy(true)
    setError('')
    try {
      const body = {
        icon_id: iconId,
        icon_bg: normalizeColour(iconBg),
        name: name.trim(),
        blurb: blurb.trim(),
        kind: isFloor ? 'boolean' : kind,
        group,
        unit: !isFloor && kind === 'quantity' ? unit.trim() : '',
        addon_sku: isFloor || kind === 'access' ? '' : addonSku.trim(),
        levels: !isFloor && kind === 'level' ? levels : [],
        teaser: !isFloor && teaser,
        sort_order: Number(sortOrder || 0),
      }
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
      title={feature ? `Edit ${feature.name}` : isFloor ? 'Add floor item' : 'Add feature'}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="feature-form" disabled={busy || Boolean(problem)}>
            {feature ? 'Save' : isFloor ? 'Add floor item' : 'Add feature'}
          </button>
        </>
      }
    >
      <form id="feature-form" onSubmit={(e) => void submit(e)} className="stack tight" data-testid="feature-editor">
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <FormRow>
          <Field label="Key" help={feature ? 'Set once; the matrix, the add-ons and the invoices name it.' : 'backup, ssl, bandwidth — lower-case, stable.'}>
            <input className="mono" value={key} onChange={(e) => setKey(e.target.value)} disabled={Boolean(feature)} autoFocus={!feature} />
          </Field>
          <Field label="Name">
            <input value={name} onChange={(e) => setName(e.target.value)} autoFocus={Boolean(feature)} />
          </Field>
        </FormRow>
        <Field label="Blurb" help="One line a prospect reads under the name — no number that is not in the workbook.">
          <input value={blurb} onChange={(e) => setBlurb(e.target.value)} />
        </Field>
        <FormRow>
          <Field label="Group" help={isFloor ? 'On every package, never priced, no cell.' : 'Where the row sits when a package is read.'}>
            <select value={group} onChange={(e) => setGroup(e.target.value)} aria-label="Group">
              <option value="floor">Floor — on every package</option>
              {groups.map((g) => (
                <option key={g.key} value={g.key}>
                  {g.name}
                </option>
              ))}
            </select>
          </Field>
          {!isFloor ? (
            <Field label="Kind" help={KINDS.find((k) => k.value === kind)?.help}>
              <select value={kind} onChange={(e) => setKind(e.target.value as FeatureKind)} aria-label="Kind">
                {KINDS.map((k) => (
                  <option key={k.value} value={k.value}>
                    {k.label}
                  </option>
                ))}
              </select>
            </Field>
          ) : null}
          <Field label="Sort order">
            <input type="number" step={1} inputMode="numeric" value={sortOrder} onChange={(e) => setSortOrder(e.target.value)} />
          </Field>
        </FormRow>
        {!isFloor ? (
          <FormRow>
            {kind === 'quantity' ? (
              <Field label="Unit">
                <input value={unit} onChange={(e) => setUnit(e.target.value)} placeholder="Mbps" />
              </Field>
            ) : null}
            {kind !== 'access' ? (
              <Field label="Add-on SKU" help={kind === 'quantity' ? 'The metered SKU the included quantity is an allowance on.' : kind === 'level' ? 'The SKU billed when a package offers the next level as an add-on.' : 'The SKU billed when a package offers the feature as an add-on.'}>
                <input className="mono" value={addonSku} onChange={(e) => setAddonSku(e.target.value)} placeholder={kind === 'quantity' ? 'eip.bandwidth_mbps' : 'addon.backup'} />
              </Field>
            ) : null}
            <label className="check">
              <input type="checkbox" checked={teaser} onChange={(e) => setTeaser(e.target.checked)} aria-label="Teaser" /> Teaser — a package without it says which package has it
            </label>
          </FormRow>
        ) : null}
        {!isFloor && kind === 'level' ? (
          <Field label="Levels" help="One per line, lowest first. A package is at one of them; the next may be purchasable.">
            <textarea value={levelsText} onChange={(e) => setLevelsText(e.target.value)} rows={4} aria-label="Levels" placeholder={'weekly · 7 days\ndaily · 14 days\ndaily · 30 days'} />
          </Field>
        ) : null}
        <FormRow>
          <IconField value={iconId} onChange={setIconId} bg={iconBg} help="Shown beside the name on the storefront, the calculator and here. SVG, PNG or WebP, up to 64 KiB." />
          <ColourField label="Icon background" value={iconBg} onChange={setIconBg} help="The tile behind the icon; empty = none." />
        </FormRow>
        {problem ? <div className="err small">{problem}</div> : null}
      </form>
    </Modal>
  )
}

/** The settings of one package: tagline, recommended, the term rule, the shape and the column's branding (icon, accent, badge), written whole. */
export function PackageSettingsModal({ book, pkg, currency, onClose, onSaved }: { book: PriceBook; pkg: PackageInfo; currency: string; onClose: () => void; onSaved: () => void | Promise<void> }) {
  const sh = pkg.shape ?? {}
  const str = (v: number | string | undefined | null) => (v === undefined || v === null ? '' : String(v))
  const [tagline, setTagline] = useState(pkg.tagline ?? '')
  const [recommended, setRecommended] = useState(Boolean(pkg.recommended))
  const [months, setMonths] = useState(String(pkg.annual_months_free ?? 0))
  const [vcpu, setVcpu] = useState(str(sh.vcpu))
  const [mem, setMem] = useState(str(sh.memory_gb))
  const [vcpuG, setVcpuG] = useState(str(sh.vcpu_guaranteed))
  const [memG, setMemG] = useState(str(sh.memory_gb_guaranteed))
  const [disk, setDisk] = useState(str(sh.disk_gb))
  const [iconId, setIconId] = useState(iconIdOf(pkg.icon))
  const [accent, setAccent] = useState(pkg.accent ?? '')
  const [badge, setBadge] = useState(pkg.badge ?? '')
  // DESIGN.md §22.11 — grow: allowed, the ceiling per dimension and the
  // package's own compute overage rates. Part of the whole write: a save
  // that left them out would switch grow off.
  const grow = pkg.grow
  const rateOf = (key: string) => grow?.overage_rates?.find((r) => r.key === key)?.price_month ?? ''
  const [growAllowed, setGrowAllowed] = useState(Boolean(grow?.allowed))
  const [gVcpu, setGVcpu] = useState(str(grow?.ceiling?.vcpu))
  const [gMem, setGMem] = useState(str(grow?.ceiling?.memory_gb))
  const [gDisk, setGDisk] = useState(str(grow?.ceiling?.disk_gb))
  const [gBw, setGBw] = useState(str(grow?.ceiling?.bandwidth_mbps))
  const [oVcpu, setOVcpu] = useState(rateOf('vcpu'))
  const [oMem, setOMem] = useState(rateOf('memory'))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const nonNeg = (v: string) => v.trim() === '' || (Number.isFinite(Number(v)) && Number(v) >= 0)
  const problem = (() => {
    const m = Number(months)
    if (months.trim() === '' || !Number.isInteger(m) || m < 0 || m > 12) return 'the months free on an annual term are a whole number between 0 and 12'
    if (![vcpu, mem, vcpuG, memG, disk].every(nonNeg)) return 'every shape value is a non-negative number, or empty'
    if (!isColour(accent)) return 'the accent is a colour written #RRGGBB'
    if (growAllowed && ![gVcpu, gMem, gDisk, gBw, oVcpu, oMem].every((v) => v.trim() !== '' && nonNeg(v))) return 'grow needs the four ceilings and the two overage rates'
    return badgeProblem(badge)
  })()
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (problem) return
    const num = (v: string) => (v.trim() === '' ? null : v.trim())
    const body: PackageSettingsWrite = { tagline: tagline.trim(), recommended, annual_months_free: Number(months), vcpu: num(vcpu), memory_gb: num(mem), vcpu_guaranteed: num(vcpuG), memory_gb_guaranteed: num(memG), disk_gb: num(disk), icon_id: iconId, accent: normalizeColour(accent), badge: badge.trim(),
      grow_allowed: growAllowed,
      ...(growAllowed ? { grow_ceiling_vcpu: num(gVcpu), grow_ceiling_memory_gb: num(gMem), grow_ceiling_disk_gb: num(gDisk), grow_ceiling_bandwidth_mbps: num(gBw), overage_vcpu_month: num(oVcpu), overage_mem_gb_month: num(oMem) } : {}) }
    setBusy(true)
    setError('')
    try {
      await api.put(`/pricebooks/${book.id}/packages/${encodeURIComponent(pkg.sku)}/settings`, body)
      await onSaved()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title={`${pkg.name} — package settings`}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="package-settings-form" disabled={busy || Boolean(problem)}>
            Save
          </button>
        </>
      }
    >
      <form id="package-settings-form" onSubmit={(e) => void submit(e)} className="stack tight" data-testid="package-settings-editor">
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <p className="muted small" style={{ margin: 0 }}>
          {pkg.price_month} {currency} / month — the price is the plan item on the Items tab. What is set here is published with the package.
        </p>
        <FormRow>
          <Field label="Tagline" help="Under the name on the storefront; empty when the workbook has none.">
            <input value={tagline} onChange={(e) => setTagline(e.target.value)} aria-label="Tagline" />
          </Field>
          <Field label="Months free on an annual term" help="0 = no annual term rule.">
            <input type="number" min={0} max={12} step={1} inputMode="numeric" value={months} onChange={(e) => setMonths(e.target.value)} aria-label="Annual months free" />
          </Field>
          <label className="check">
            <input type="checkbox" checked={recommended} onChange={(e) => setRecommended(e.target.checked)} aria-label="Recommended" /> Recommended
          </label>
        </FormRow>
        <FormRow>
          <Field label="vCPU" help="The headline.">
            <input type="number" min={0} step="any" inputMode="decimal" value={vcpu} onChange={(e) => setVcpu(e.target.value)} aria-label="vCPU" />
          </Field>
          <Field label="Memory (GB)">
            <input type="number" min={0} step="any" inputMode="decimal" value={mem} onChange={(e) => setMem(e.target.value)} aria-label="Memory GB" />
          </Field>
          <Field label="Disk (GB)">
            <input type="number" min={0} step="any" inputMode="decimal" value={disk} onChange={(e) => setDisk(e.target.value)} aria-label="Disk GB" />
          </Field>
        </FormRow>
        <FormRow>
          <Field label="vCPU guaranteed" help="The floor under the headline (headline ÷ overcommit).">
            <input type="number" min={0} step="any" inputMode="decimal" value={vcpuG} onChange={(e) => setVcpuG(e.target.value)} aria-label="vCPU guaranteed" />
          </Field>
          <Field label="Memory guaranteed (GB)">
            <input type="number" min={0} step="any" inputMode="decimal" value={memG} onChange={(e) => setMemG(e.target.value)} aria-label="Memory GB guaranteed" />
          </Field>
        </FormRow>
        <FormRow>
          <IconField value={iconId} onChange={setIconId} help="Beside the package's name in its column." />
          <ColourField label="Accent" value={accent} onChange={setAccent} help="The column's top border and its badge." />
          <Field label="Badge" help={`A short chip over the column — “Most popular”. At most ${BADGE_MAX} characters; empty = none.`}>
            <input value={badge} onChange={(e) => setBadge(e.target.value)} maxLength={BADGE_MAX} aria-label="Badge" />
          </Field>
        </FormRow>
        <label className="check">
          <input type="checkbox" checked={growAllowed} onChange={(e) => setGrowAllowed(e.target.checked)} aria-label="Grow allowed" /> Grow allowed — a customer may raise the quota to the ceiling and pay the usage above the package in arrears
        </label>
        {growAllowed ? (
          <>
            <FormRow>
              <Field label="Ceiling vCPU" help="The most grow raises the quota to.">
                <input type="number" min={0} step="any" inputMode="decimal" value={gVcpu} onChange={(e) => setGVcpu(e.target.value)} aria-label="Ceiling vCPU" />
              </Field>
              <Field label="Ceiling memory (GB)">
                <input type="number" min={0} step="any" inputMode="decimal" value={gMem} onChange={(e) => setGMem(e.target.value)} aria-label="Ceiling memory GB" />
              </Field>
              <Field label="Ceiling disk (GB)">
                <input type="number" min={0} step="any" inputMode="decimal" value={gDisk} onChange={(e) => setGDisk(e.target.value)} aria-label="Ceiling disk GB" />
              </Field>
              <Field label="Ceiling bandwidth (Mbps)">
                <input type="number" min={0} step="any" inputMode="decimal" value={gBw} onChange={(e) => setGBw(e.target.value)} aria-label="Ceiling bandwidth Mbps" />
              </Field>
            </FormRow>
            <FormRow>
              <Field label={`vCPU above the package (${currency} / vCPU / month)`} help="The package's own rate; disk and bandwidth are the book's meter prices.">
                <input type="number" min={0} step="any" inputMode="decimal" value={oVcpu} onChange={(e) => setOVcpu(e.target.value)} aria-label="Overage vCPU per month" />
              </Field>
              <Field label={`Memory above the package (${currency} / GB / month)`}>
                <input type="number" min={0} step="any" inputMode="decimal" value={oMem} onChange={(e) => setOMem(e.target.value)} aria-label="Overage memory per GB per month" />
              </Field>
            </FormRow>
          </>
        ) : null}
        {problem ? <div className="err small">{problem}</div> : null}
      </form>
    </Modal>
  )
}

/** The icon of one group heading (PUT /feature-groups/{key}); the group's name stays the product's. */
export function GroupIconModal({ group, onClose, onSaved }: { group: FeatureGroup; onClose: () => void; onSaved: () => void | Promise<void> }) {
  const [iconId, setIconId] = useState(iconIdOf(group.icon) || group.icon_id || '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api.put(`/feature-groups/${encodeURIComponent(group.key)}`, { icon_id: iconId })
      await onSaved()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title={`${group.name} — group icon`}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="group-icon-form" disabled={busy}>
            Save
          </button>
        </>
      }
    >
      <form id="group-icon-form" onSubmit={(e) => void submit(e)} className="stack tight" data-testid="group-icon-editor">
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <IconField value={iconId} onChange={setIconId} help="Shown beside the group's heading on the storefront, the calculator and here." />
      </form>
    </Modal>
  )
}
