import { useState } from 'react'
import type { PackagesDoc } from '../../api/types'
import { cellOf, includedFromText, includesRows, includesValue } from '../../lib/packages'

/**
 * The package comparison table of the public calculator (DESIGN.md §12.5,
 * §22): S / M / L / XL as columns with their price per month, the included
 * quantities, then the feature matrix — ✓ Included, "+ price" Optional with a
 * tick box and an "included from XL" hint, — not offered. Choosing a package
 * adds its plan line; the optional features ticked under it add their add-on
 * lines. Every figure is the server's document; nothing here is arithmetic.
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
  const rows = includesRows(doc)
  const slugOf = (sku: string) => sku.replace(/^plan\./, '')
  return (
    <div className="table-wrap" data-testid="package-table">
      <table className="pkg-table compare" aria-label="Package comparison">
        <thead>
          <tr>
            <th></th>
            {doc.packages.map((p) => (
              <th key={p.sku} className="pkg-head">
                <div className="pkg-name">{p.name}</div>
                <div className="pkg-price num">
                  <b>{p.price_month}</b>
                </div>
                <div className="tiny muted">{currency} / month</div>
                <button type="button" className="primary small" onClick={() => onChoose(slugOf(p.sku), picked[p.sku] ?? [])} aria-label={`Choose ${p.name}`}>
                  Choose
                </button>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.key} data-testid={`includes-${r.key}`}>
              <td>{r.label}</td>
              {doc.packages.map((p) => (
                <td key={p.sku} className="num center">
                  {includesValue(p, r.key)}
                </td>
              ))}
            </tr>
          ))}
          {doc.features.map((f) => (
            <tr key={f.key} data-testid={`compare-${f.key}`}>
              {/* The blurb is the tooltip: four package columns leave the
                  feature column no room for a sentence under every name. */}
              <td className="pkg-feature" title={f.blurb || undefined}>
                {f.name}
              </td>
              {doc.packages.map((p) => {
                const c = cellOf(f, p.sku)
                if (c.state === 'included') {
                  return (
                    <td key={p.sku} className="center pkg-included">
                      {f.kind === 'quantity' && c.quantity !== undefined && c.quantity !== null && c.quantity !== '' ? (
                        <span>
                          {c.quantity} {f.unit ?? ''}
                        </span>
                      ) : (
                        <span aria-label="Included">✓ Included</span>
                      )}
                    </td>
                  )
                }
                if (c.state === 'optional' && c.addon_sku) {
                  const on = (picked[p.sku] ?? []).includes(f.key)
                  const hint = includedFromText(doc, c)
                  return (
                    <td key={p.sku} className="center pkg-optional">
                      <label className="check pkg-tick">
                        <input type="checkbox" checked={on} onChange={() => toggle(p.sku, f.key)} aria-label={`${f.name} on ${p.name}`} disabled={!c.price_month} />
                        <span className="num">{c.price_month ? `+ ${c.price_month}` : 'ask us'}</span>
                      </label>
                      <div className="tiny muted pkg-hint">
                        {c.price_month ? <div>{currency} / month</div> : null}
                        {hint ? <div>{hint}</div> : null}
                      </div>
                    </td>
                  )
                }
                return (
                  <td key={p.sku} className="center muted pkg-none">
                    <span aria-label="Not offered">—</span>
                  </td>
                )
              })}
            </tr>
          ))}
        </tbody>
      </table>
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
