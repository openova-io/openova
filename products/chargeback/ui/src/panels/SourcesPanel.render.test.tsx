import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import type { CostSource, PriceBook } from '../api/types'
import { SourcesPanel } from './SourcesPanel'

/**
 * The Sources tab, rendered (DESIGN.md §2, founder direction 2026-09-08).
 * The pure rules live in lib/layers.test.ts; this asserts the PIXELS: a
 * layer badge on every row, and a price-book select that offers only the
 * books whose scope matches that row's layer — the assignment the server
 * would otherwise answer 400 for is not even offered.
 */

const books: PriceBook[] = [
  { id: 'cloud-1', name: 'National Cloud list', scope: 'cloud', currency: 'OMR', annual_divisor: 8760, bill_stopped: 'compute' },
  { id: 'plans', name: 'OpenOva plans', scope: 'platform', currency: 'OMR', annual_divisor: 8760, bill_stopped: 'compute' },
]

const sources: CostSource[] = [
  { id: 's-cloud', customer_id: 'c1', kind: 'huawei-project', layer: 'cloud', price_book_id: 'cloud-1', price_book_name: 'National Cloud list', region: 'me-east-215', project_id: 'proj-a', status: 'verified' },
  { id: 's-plat', customer_id: 'c1', kind: 'openova-org', layer: 'platform', price_book_id: null, region: '', project_id: 'acme', status: 'verified' },
]

function render(props: Partial<Parameters<typeof SourcesPanel>[0]> = {}): string {
  const html = renderToString(
    createElement(
      MemoryRouter,
      null,
      createElement(SourcesPanel, {
        customerId: 'c1',
        sources,
        books,
        canManage: true,
        canRotate: true,
        onChanged: () => {},
        ...props,
      }),
    ),
  ).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('SourcesPanel renders the two layers', () => {
  it('badges every row with its layer', () => {
    const html = render()
    expect(html).toContain('>Layer<')
    expect(html).toContain('Cloud')
    expect(html).toContain('Platform')
    expect(html).toContain('Huawei project')
    expect(html).toContain('OpenOva Organization')
  })

  it('offers each source only the books of its own scope', () => {
    const html = render()
    // Two selects, one per source, each labelled by its source.
    expect(html).toContain('aria-label="Price book for proj-a"')
    expect(html).toContain('aria-label="Price book for acme"')
    const cloudSelect = html.slice(html.indexOf('aria-label="Price book for proj-a"'))
    const cloudOptions = cloudSelect.slice(0, cloudSelect.indexOf('</select>'))
    expect(cloudOptions).toContain('National Cloud list')
    expect(cloudOptions).not.toContain('OpenOva plans')
    const platSelect = html.slice(html.indexOf('aria-label="Price book for acme"'))
    const platOptions = platSelect.slice(0, platSelect.indexOf('</select>'))
    expect(platOptions).toContain('OpenOva plans')
    expect(platOptions).not.toContain('National Cloud list')
  })

  it('warns on a source with no book, because its usage rates to 0', () => {
    expect(render()).toContain('its usage rates to 0')
  })

  it('a read-only lens shows the book as text, never a select', () => {
    const html = render({ canManage: false, canRotate: false })
    expect(html).toContain('National Cloud list')
    expect(html).not.toContain('aria-label="Price book for proj-a"')
    expect(html).toContain('no price book')
  })

  it('the internal platform source is never billed and takes no select', () => {
    const html = render({
      canManage: true,
      sources: [{ id: 's-int', kind: 'openova-platform', layer: 'platform', internal: true, price_book_id: null, region: '', project_id: 'hw307-omani-works', status: 'verified' } as CostSource],
    })
    expect(html).toContain('Platform overhead (this Sovereign)')
    expect(html).toContain('not billed')
    expect(html).not.toContain('aria-label="Price book for hw307-omani-works"')
  })

  it('badges a disabled source and offers Enable, never Verify', () => {
    const html = render({
      sources: [{ ...sources[0], status: 'disabled' } as CostSource],
    })
    expect(html).toContain('badge')
    expect(html).toContain('disabled')
    expect(html).toContain('decommissioned — collects nothing new; its history still bills')
    expect(html).toContain('>Enable<')
    expect(html).not.toContain('>Verify<')
  })

  it('offers Disable on a live source, and never on the internal one', () => {
    // The row's action cell, not the legend below the table — which names
    // Disable in prose for every lens.
    const actions = (html: string) => html.slice(html.indexOf('nowrap actions'), html.indexOf('</table>'))
    expect(actions(render())).toContain('>Disable<')
    const internal = render({
      sources: [{ id: 's-int', kind: 'openova-platform', layer: 'platform', internal: true, price_book_id: null, region: '', project_id: 'hw307', status: 'verified' } as CostSource],
    })
    expect(actions(internal)).not.toContain('>Disable<')
  })

  it('the add-source form offers cloud kinds only — platform sources are automatic', () => {
    const html = render({ autoAdd: true })
    expect(html).toContain('Add cost source')
    expect(html).toContain('Huawei project')
    expect(html).toContain('File import')
    // The kinds the Organization sync owns are not on offer.
    const form = html.slice(html.indexOf('id="source-form"'))
    expect(form).not.toContain('OpenOva Organization')
    expect(form).not.toContain('Kubernetes namespace')
    // Its book select is the CLOUD catalogue only; the sentence below it
    // may name the plans book, but the select must never offer it.
    const select = form.slice(form.indexOf('aria-label="Price book"'))
    const options = select.slice(0, select.indexOf('</select>'))
    expect(options).toContain('National Cloud list')
    expect(options).not.toContain('OpenOva plans')
    expect(form).toContain('created automatically by the Organization sync')
  })
})
