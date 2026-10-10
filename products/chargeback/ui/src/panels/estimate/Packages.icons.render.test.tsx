import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ICON_BACKUP, ICON_PLAN_M, ICON_RESILIENCE, ICON_SSL, brandedPackages, packages } from './fixture'
import { PackageTable } from './Packages'

/**
 * The calculator's comparison table draws the icons and branding the
 * document names and nothing else (DESIGN.md §22.10): every `src` resolved
 * against the URL the document was read from, a feature's tile colour, the
 * floor item's and the group's icons, a package's accent as its column's top
 * border and its badge as a chip. An item without an icon draws no box at
 * all, so the unbranded document renders exactly as before.
 */

const DOC_URL = 'https://chargeback.t99.omani.works/api/v1/public/packages'
const render = (doc = brandedPackages) => renderToString(createElement(PackageTable, { doc, currency: 'OMR', onChoose: () => {}, documentUrl: DOC_URL }))

describe('the comparison table, branded', () => {
  it('draws each icon from the API origin, the tile under a feature, the accent and the badge', () => {
    const html = render()
    const at = (id: string) => `https://chargeback.t99.omani.works/api/v1/public/icons/${id}`
    // The feature row: its icon on its tile, before its name.
    expect(html).toMatch(new RegExp(`data-testid="compare-backup"><td class="pkg-feature"[^>]*><span class="pkg-feature-name"><span class="pkg-icon tiled" style="background:#FFE4E6"[^>]*><img src="${at(ICON_BACKUP)}" alt=""`))
    // The floor item and the group heading: an icon, no tile.
    expect(html).toMatch(new RegExp(`data-testid="compare-floor-ssl"><span class="pkg-icon" data-testid="pkg-icon" aria-hidden="true"><img src="${at(ICON_SSL)}"`))
    expect(html).toMatch(new RegExp(`data-testid="compare-group-resilience"><th[^>]*><span class="pkg-group-name"><span class="pkg-icon"[^>]*><img src="${at(ICON_RESILIENCE)}"`))
    // M: the accent as the column's top border, the badge in the accent, the icon by the name.
    expect(html).toMatch(/class="pkg-head recommended accented" style="border-top-color:#3B82F6" data-testid="compare-head-plan\.m"><div class="pkg-badge" style="background:#3B82F6" data-testid="compare-badge-plan\.m">Most popular<\/div>/)
    expect(html).toContain(`<img src="${at(ICON_PLAN_M)}"`)
    // S: an accent and no badge; L: nothing.
    expect(html).toMatch(/class="pkg-head accented" style="border-top-color:#93C5FD" data-testid="compare-head-plan\.s">/)
    expect(html).not.toContain('compare-badge-plan.s')
    expect(html).toMatch(/class="pkg-head" data-testid="compare-head-plan\.l">/)
    // Exactly the four icons the document names — nothing for the rest.
    expect(html.match(/data-testid="pkg-icon"/g)).toHaveLength(4)
    expect((html.match(/<img /g) ?? []).length).toBe(4)
  })

  it('draws no icon box, accent or badge from a document that names none', () => {
    const html = render(packages)
    expect(html).not.toContain('pkg-icon')
    expect(html).not.toContain('<img')
    expect(html).not.toContain('accented')
    expect(html).not.toContain('pkg-badge')
    // The feature names are where they were, inside the same cell.
    expect(html).toMatch(/data-testid="compare-backup"><td class="pkg-feature"[^>]*><span class="pkg-feature-name">Backup<\/span>/)
  })
})
