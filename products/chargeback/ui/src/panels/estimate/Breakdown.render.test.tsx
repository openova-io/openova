import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { Breakdown } from './Breakdown'
import { familyColor, type FamilyShare } from './model'

/**
 * The per-family breakdown (DESIGN.md §12.5) rendered: one bar segment per
 * family whose width is its share, in the family's palette colour; a legend
 * with each family's figure and percentage; a gauge whose ring is the
 * largest share; nothing at all until there is something to show.
 */

const shares: FamilyShare[] = [
  { family: 'compute', familyName: 'Compute', amount: '70.000000', share: 0.7, color: familyColor('compute') },
  { family: 'storage', familyName: 'Storage', amount: '30.000000', share: 0.3, color: familyColor('storage') },
]

const render = (s: FamilyShare[]) => renderToString(createElement(Breakdown, { shares: s, currency: 'OMR' })).replace(/<!-- -->/g, '')

describe('the family breakdown', () => {
  it('draws one segment per family, as wide as its share, in the family colour', () => {
    const html = render(shares)
    const segments = [...html.matchAll(/<i style="width:([\d.]+%);background:(#[0-9a-f]{6})"/g)].map((m) => [m[1], m[2]])
    expect(segments).toEqual([
      ['70.00%', familyColor('compute')],
      ['30.00%', familyColor('storage')],
    ])
    expect(familyColor('compute')).not.toBe(familyColor('storage'))
    // Colour follows the family: Other services is always the neutral.
    expect(familyColor('other')).toBe('#94a3b8')
  })

  it('names each family with its figure and share in the legend', () => {
    const html = render(shares)
    expect(html).toContain('Compute <span class="num">70.000 OMR</span> <span class="muted">70 %</span>')
    expect(html).toContain('Storage <span class="num">30.000 OMR</span> <span class="muted">30 %</span>')
    expect(html).toContain('Compute is the largest share of 2 families')
    expect(html).not.toMatch(/NaN|undefined/)
  })

  it('gauges the largest family: the ring is its share and the label says so', () => {
    const html = render(shares)
    expect(html).toContain('aria-label="Compute is 70 % of the estimate"')
    expect(html).toContain(`conic-gradient(${familyColor('compute')} 0 70.00%, var(--line) 0)`)
    expect(html).toContain('<span>70 %</span>')
    const one = render([shares[0]])
    expect(one).toContain('Compute is the largest share</div>')
    expect(one).toContain('<span>70 %</span>')
  })

  it('renders nothing until there is something to break down', () => {
    expect(render([])).toBe('')
  })
})
