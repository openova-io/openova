import type { PackagesDoc } from '../../api/types'
import { formatMoney } from '../../lib/money'
import { unitHint, type CatalogFamily, type CatalogService } from './model'
import { PackageTable } from './Packages'

/**
 * The service catalogue (DESIGN.md §12.5): product families down the left,
 * each a card with a glyph and a one-line description, its services a row
 * each with a friendly name, a one-line description, the cheapest monthly
 * figure the server priced for one unit, and the button that opens its
 * configurator. No SKU is shown here. The Platform plans family is the
 * package comparison table (DESIGN.md §22) when the Sovereign publishes one.
 */
export function Catalogue({
  families,
  currency,
  query,
  onQuery,
  onChoose,
  packages,
  onChoosePackage,
}: {
  families: CatalogFamily[]
  currency: string
  query: string
  onQuery: (q: string) => void
  onChoose: (service: CatalogService) => void
  /** GET /public/packages, when published; the plans family then draws the comparison table. */
  packages?: PackagesDoc | null
  onChoosePackage?: (slug: string, addons: string[]) => void
}) {
  return (
    <div className="stack">
      <div className="toolbar">
        <div className="field grow">
          <label htmlFor="catalogue-search">Find a service</label>
          <input id="catalogue-search" placeholder="Search by name — servers, database, backup…" value={query} onChange={(e) => onQuery(e.target.value)} />
        </div>
      </div>
      {families.length === 0 ? (
        <div className="card">
          <p className="muted">Nothing matches “{query}”.</p>
        </div>
      ) : null}
      {families.map((fam) => (
        <div key={fam.key} className="card" data-testid={`family-${fam.key}`}>
          <div className="card-head">
            <div className="fam-head">
              <span className="fam-glyph" data-family={fam.key} aria-hidden />
              <div style={{ minWidth: 0 }}>
                <h2>{fam.name}</h2>
                {fam.blurb ? <div className="sub">{fam.blurb}</div> : null}
              </div>
            </div>
            <span className="hint">
              {fam.key === 'plans' && packages && onChoosePackage ? `${packages.packages.length} package${packages.packages.length === 1 ? '' : 's'}` : `${fam.services.length} service${fam.services.length === 1 ? '' : 's'}`}
            </span>
          </div>
          {fam.key === 'plans' && packages && packages.packages.length && onChoosePackage ? (
            <PackageTable
              doc={packages}
              currency={currency}
              onChoose={onChoosePackage}
              onConfigure={() => {
                const plan = fam.services.find((s) => s.key === 'plan')
                if (plan) onChoose(plan)
              }}
            />
          ) : (
          <div className="strip">
            {fam.services.map((svc) => (
              <div key={svc.key} className="strip-row" data-testid={`service-${svc.key}`}>
                <div>
                  <strong>{svc.name}</strong>
                  {svc.from ? (
                    <div className="small muted num">
                      from {formatMoney(svc.from.monthly, currency)} / month {unitHint(svc.from.unit)}
                    </div>
                  ) : null}
                </div>
                <div className="small muted">{svc.blurb || svc.name}</div>
                <button className="small" onClick={() => onChoose(svc)} aria-label={`Configure ${svc.name}`}>
                  Configure
                </button>
              </div>
            ))}
          </div>
          )}
        </div>
      ))}
    </div>
  )
}
