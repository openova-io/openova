import { useState } from 'react'
import type { PackageCell, PackageFeature, PackagesDoc } from '../../api/types'
import { cellOf, floorItems, groupedFeatures, includedFromText, levelLabel, nextLevelLabel, shapeGuaranteed, shapeHeadline, stepUpHint, teaserText } from '../../lib/packages'

/**
 * The package comparison table of the public calculator (DESIGN.md §12.5,
 * §22.6): S / M / L / XL as columns — price, tagline, the shape with the
 * guaranteed floors in small type, the Recommended badge — then the
 * features grouped under their group heading: ✓ Included, "+ price" Optional
 * with a tick box and an "included from XL" hint, a quantity with what
 * happens above it, a level's label (and its purchasable next level), an
 * access door's ✓ / —, "from XL" on a teaser, — where not offered. The floor
 * is one strip under the table. When the add-ons ticked under a package are
 * all included on the next package and are worth the step, the STEP-UP HINT
 * says so with a button that chooses the next package instead. Choosing a
 * package adds its plan line; the add-ons ticked under it add their add-on
 * lines. Every figure is the server's document; the only arithmetic here
 * adds the ticked add-on prices for the hint.
 *
 * Layout (0.1.62): the table sits in the family card at a fixed layout —
 * the feature column takes 28 %, the four packages share the rest — so all
 * four columns are inside the card from 1280 px up (seen live at 0.1.61:
 * the auto layout let the no-wrap headers push XL past the card's edge).
 * Under 1100 px the table keeps a 900 px floor inside its own scroll
 * container; the floor strip and the footer stay outside it, full width.
 * Measured in tests/e2e/playwright/tests/chargeback-calculator-ladder.spec.ts.
 */
export function PackageTable({
  doc,
  currency,
  onChoose,
  onConfigure,
}: {
  doc: PackagesDoc
  currency: string
  onChoose: (slug: string, addons: string[]) => void
  /** Opens the plans configurator (months, Organizations), for the prospect who wants more than one or a term. */
  onConfigure?: () => void
}) {
  const [picked, setPicked] = useState<Record<string, string[]>>({})
  const toggle = (planSku: string, key: string) =>
    setPicked((p) => {
      const cur = p[planSku] ?? []
      return { ...p, [planSku]: cur.includes(key) ? cur.filter((k) => k !== key) : [...cur, key] }
    })
  const slugOf = (sku: string) => sku.replace(/^plan\./, '')
  const groups = groupedFeatures(doc)
  const floor = floorItems(doc)
  // The add-ons the next package does not include stay ticked on it.
  const chooseNext = (planSku: string, nextSku: string) => {
    const keep = (picked[planSku] ?? []).filter((k) => {
      const f = doc.features.find((x) => x.key === k)
      const c = f ? cellOf(f, nextSku) : null
      return c?.state === 'optional' || Boolean(c?.next_level_addon)
    })
    onChoose(slugOf(nextSku), keep)
  }
  return (
    <div className="pkg-compare" data-testid="package-table">
      <div className="table-wrap pkg-compare-scroll" data-testid="package-table-scroll">
        <table className="pkg-table compare" aria-label="Package comparison">
          <colgroup>
            <col className="pkg-col-feature" />
            {doc.packages.map((p) => (
              <col key={p.sku} className="pkg-col-package" />
            ))}
          </colgroup>
          <thead>
            <tr>
              <th></th>
              {doc.packages.map((p) => {
                const guaranteed = shapeGuaranteed(p)
                return (
                  <th key={p.sku} className={`pkg-head${p.recommended ? ' recommended' : ''}`}>
                    {p.recommended ? <div className="badge ok pkg-recommended">Recommended</div> : null}
                    <div className="pkg-name">{p.name}</div>
                    {p.tagline ? <div className="tiny muted pkg-tagline">{p.tagline}</div> : null}
                    <div className="pkg-price num">
                      <b>{p.price_month}</b>
                    </div>
                    <div className="tiny muted">{currency} / month</div>
                    <div className="tiny pkg-shape">{shapeHeadline(p)}</div>
                    {guaranteed ? <div className="tiny muted pkg-guaranteed">{guaranteed}</div> : null}
                    <button type="button" className="primary small" onClick={() => onChoose(slugOf(p.sku), picked[p.sku] ?? [])} aria-label={`Choose ${p.name}`}>
                      Choose
                    </button>
                  </th>
                )
              })}
            </tr>
          </thead>
          <tbody>
            {groups.map((g) => (
              <GroupBody key={g.group.key} name={g.group.name} groupKey={g.group.key} features={g.features} doc={doc} currency={currency} picked={picked} onToggle={toggle} />
            ))}
            <tr className="pkg-stepup-row" data-testid="step-up-hints">
              <td></td>
              {doc.packages.map((p) => {
                const hint = stepUpHint(doc, p.sku, picked[p.sku] ?? [])
                return (
                  <td key={p.sku} className="center">
                    {hint ? (
                      <div className="pkg-stepup-hint" data-testid={`step-up-hint-${p.sku}`}>
                        <div className="tiny">
                          {hint.next_name} includes all of this for {hint.gap_month} {currency} more
                        </div>
                        <button type="button" className="small" onClick={() => chooseNext(p.sku, hint.next_sku)} aria-label={`Choose ${hint.next_name} instead of ${p.name}`}>
                          Choose {hint.next_name} instead
                        </button>
                      </div>
                    ) : null}
                  </td>
                )
              })}
            </tr>
          </tbody>
        </table>
      </div>
      {floor.length ? (
        <div className="pkg-floor-strip" data-testid="floor-strip">
          <span className="muted small">On every package:</span>{' '}
          {floor.map((f, i) => (
            <span key={f.key} className="pkg-floor-item" title={f.blurb || undefined}>
              {i > 0 ? ' · ' : ''}
              {f.name}
            </span>
          ))}
        </div>
      ) : null}
      <div className="row between small muted" style={{ marginTop: 8 }}>
        <span>Tick the add-ons you want under a package, then choose it. More than one Organization, or a term of several months: configure the plan.</span>
        {onConfigure ? (
          <button type="button" className="small" onClick={onConfigure} aria-label="Configure Platform plans">
            Configure
          </button>
        ) : null}
      </div>
    </div>
  )
}

function GroupBody({
  name,
  groupKey,
  features,
  doc,
  currency,
  picked,
  onToggle,
}: {
  name: string
  groupKey: string
  features: PackageFeature[]
  doc: PackagesDoc
  currency: string
  picked: Record<string, string[]>
  onToggle: (planSku: string, key: string) => void
}) {
  return (
    <>
      <tr className="pkg-group" data-testid={`compare-group-${groupKey}`}>
        <th colSpan={doc.packages.length + 1}>{name}</th>
      </tr>
      {features.map((f) => (
        <tr key={f.key} data-testid={`compare-${f.key}`}>
          {/* The blurb is the tooltip: four package columns leave the
              feature column no room for a sentence under every name. */}
          <td className="pkg-feature" title={f.blurb || undefined}>
            {f.name}
          </td>
          {doc.packages.map((p) => (
            <Cell key={p.sku} feature={f} cell={cellOf(f, p.sku)} planSku={p.sku} planName={p.name} doc={doc} currency={currency} on={(picked[p.sku] ?? []).includes(f.key)} onToggle={() => onToggle(p.sku, f.key)} />
          ))}
        </tr>
      ))}
    </>
  )
}

function Cell({ feature, cell, planName, doc, currency, on, onToggle }: { feature: PackageFeature; cell: PackageCell; planSku: string; planName: string; doc: PackagesDoc; currency: string; on: boolean; onToggle: () => void }) {
  const f = feature
  // A purchasable next level on a level feature is a tick like any add-on.
  const addon = f.kind === 'level' ? cell.next_level_addon : cell.state === 'optional' && cell.addon_sku ? { addon_sku: cell.addon_sku, price_month: cell.price_month } : undefined
  // One compact line, "+ 1.500 / mo": the currency is the column header's
  // ("OMR / month") and the tooltip's, so four add-on tiles fit the card.
  const addonLine = addon ? (addon.price_month ? `+ ${addon.price_month} / mo` : 'ask us') : ''
  const addonTitle = addon?.price_month ? `${addon.price_month} ${currency} a month` : undefined
  if (f.kind === 'level' && cell.level !== undefined && cell.level !== null) {
    const hint = includedFromText(doc, cell)
    return (
      <td className="center pkg-level">
        <div>{levelLabel(f, cell)}</div>
        {addon ? (
          <label className="check pkg-tick" title={addonTitle}>
            <input type="checkbox" checked={on} onChange={onToggle} aria-label={`${f.name} — ${nextLevelLabel(f, cell)} on ${planName}`} disabled={!addon.price_month} />
            <span className="num">{addonLine}</span>
          </label>
        ) : null}
        <div className="tiny muted pkg-hint">
          {addon ? <div>{nextLevelLabel(f, cell)}</div> : null}
          {!addon && hint ? <div>{hint}</div> : null}
        </div>
      </td>
    )
  }
  if (cell.state === 'included') {
    if (f.kind === 'quantity') {
      const q = cell.quantity !== undefined && cell.quantity !== null && cell.quantity !== '' ? `${cell.quantity} ${f.unit ?? ''}`.trim() : cell.overage === 'unlimited' ? 'Unlimited' : '✓'
      const above = cell.overage === 'hard_cap' ? 'hard cap' : cell.overage === 'metered' ? 'more billed per use' : cell.overage === 'unlimited' && q !== 'Unlimited' ? 'unlimited' : ''
      return (
        <td className="center pkg-included">
          <span>{q}</span>
          {above ? <div className="tiny muted pkg-hint">{above}</div> : null}
        </td>
      )
    }
    return (
      <td className="center pkg-included">
        <span aria-label="Included">{f.kind === 'access' ? '✓' : '✓ Included'}</span>
        {cell.note ? <div className="tiny muted pkg-hint">{cell.note}</div> : null}
      </td>
    )
  }
  if (addon) {
    const hint = includedFromText(doc, cell)
    return (
      <td className="center pkg-optional">
        <label className="check pkg-tick" title={addonTitle}>
          <input type="checkbox" checked={on} onChange={onToggle} aria-label={`${f.name} on ${planName}`} disabled={!addon.price_month} />
          <span className="num">{addonLine}</span>
        </label>
        {hint ? <div className="tiny muted pkg-hint">{hint}</div> : null}
      </td>
    )
  }
  if (cell.state === 'teaser') {
    return (
      <td className="center muted pkg-teaser">
        <span aria-label="Not offered">{teaserText(doc, cell)}</span>
      </td>
    )
  }
  return (
    <td className="center muted pkg-none">
      <span aria-label="Not offered">—</span>
    </td>
  )
}
