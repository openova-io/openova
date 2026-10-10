// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Estimate } from '../api/types'
import { catalog, morePlans, packages, unitPrices } from '../panels/estimate/fixture'

/**
 * The package comparison table walked in a browser document (DESIGN.md §22):
 * the plans family is the table — S / M / L / XL with their price, the
 * included quantities, the feature matrix with ✓ Included, "+ price"
 * Optional and —. Ticking Backup under M and choosing M adds the plan line
 * AND the add-on line, the "included from XL" hint sits beside the add-on,
 * the server is asked for the two lines by the month, and Edit re-opens the
 * plan configurator with the add-on ticked. The fake server prices a line as
 * quantity × 730 × months × unit price, the rule the real one applies to a
 * plan and to an add-on alike.
 */

;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const fullCatalog = { ...catalog, plans: [...catalog.plans, ...morePlans] }

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => ({
    data: path === '/public/catalog' ? fullCatalog : path === '/public/packages' ? packages : null,
    error: '',
    loading: false,
    reload: async () => {},
    setData: () => {},
  }),
}))

const posts: Array<{ path: string; body: unknown }> = []

vi.mock('../api/client', () => ({
  api: {
    post: async (path: string, body: { lines: Array<{ sku?: string; plan?: string; quantity: string; hours_per_month?: string; months?: number }> }) => {
      posts.push({ path, body })
      let subtotal = 0
      let monthlySub = 0
      const lines = body.lines.map((l) => {
        const sku = l.plan ? `plan.${l.plan}` : (l.sku ?? '')
        const months = l.months ?? 1
        const hours = l.hours_per_month !== undefined ? Number(l.hours_per_month) : 730 * months
        const rated = Number(l.quantity) * hours
        const amount = rated * unitPrices[sku]
        subtotal += amount
        monthlySub += amount / months
        return { sku, plan: l.plan, unit: 'x', quantity: l.quantity, hours: String(hours), months, rated_quantity: rated.toFixed(6), unit_price: unitPrices[sku].toFixed(8), amount: amount.toFixed(6) }
      })
      const tax = subtotal * 0.05
      const monthly = monthlySub * 1.05
      const e: Estimate = {
        id: '',
        lines,
        currency: 'OMR',
        subtotal: subtotal.toFixed(6),
        tax_rate: '0.0500',
        tax: tax.toFixed(6),
        total: (subtotal + tax).toFixed(6),
        monthly: monthly.toFixed(6),
        yearly: (monthly * 12).toFixed(6),
        price_book: catalog.price_book,
        list_prices: true,
        lead: false,
        created_at: '2026-09-11T10:00:00Z',
        valid_until: '2026-10-11T10:00:00Z',
      }
      return e
    },
  },
  errorText: (e: unknown) => String(e),
}))

import { EstimatePublic } from './EstimatePublic'

let host: HTMLDivElement
let root: Root | undefined

beforeEach(() => {
  vi.useFakeTimers()
  posts.length = 0
  host = document.createElement('div')
  document.body.appendChild(host)
})

afterEach(async () => {
  await act(async () => root?.unmount())
  host.remove()
  vi.useRealTimers()
})

async function mount() {
  await act(async () => {
    root = createRoot(host)
    root.render(
      <MemoryRouter initialEntries={['/estimate']}>
        <Routes>
          <Route path="/estimate" element={<EstimatePublic />} />
        </Routes>
      </MemoryRouter>,
    )
  })
}

function byAria(label: string): HTMLElement {
  const el = document.querySelector(`[aria-label="${label}"]`)
  if (!el) throw new Error(`no element labelled "${label}"`)
  return el as HTMLElement
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
}

async function priced() {
  await act(async () => {
    vi.advanceTimersByTime(300)
  })
  await act(async () => {
    await Promise.resolve()
  })
}

const summary = () => document.querySelector('[data-testid=estimate-summary]') as HTMLElement
const table = () => document.querySelector('[data-testid=package-table]') as HTMLElement
const dialog = () => document.querySelector('[role=dialog]')

describe('the package comparison table, walked', () => {
  it('draws S / M / L / XL with price, shape and the Recommended badge, the rows grouped, every kind of cell, and the floor once', async () => {
    await mount()
    const t = table()
    expect(t).not.toBeNull()
    // The plans family is the table, not a Configure row.
    expect(document.querySelector('[data-testid=family-plans] .strip-row')).toBeNull()
    const heads = [...t.querySelectorAll('th.pkg-head')].map((h) => h.textContent)
    expect(heads).toEqual([
      'S5.000OMR / month1 vCPU · 2 GB · 25 GB disk0.17 vCPU · 0.67 GB guaranteedChoose',
      'RecommendedM9.000OMR / month2 vCPU · 4 GB · 50 GB disk0.33 vCPU · 1.33 GB guaranteedChoose',
      'L16.000OMR / month4 vCPU · 8 GB · 100 GB disk0.67 vCPU · 2.67 GB guaranteedChoose',
      'XL30.000OMR / month8 vCPU · 16 GB · 250 GB disk1.33 vCPU · 5.33 GB guaranteedChoose',
    ])
    expect(t.querySelector('th.pkg-head.recommended .pkg-recommended')?.textContent).toBe('Recommended')
    expect([...t.querySelectorAll('th.pkg-head > button')].map((b) => b.getAttribute('aria-label'))).toEqual(['Choose S', 'Choose M', 'Choose L', 'Choose XL'])
    // The rows grouped under their headings, in the document's order.
    const order = [...t.querySelectorAll('tbody tr')].map((r) => r.getAttribute('data-testid'))
    expect(order).toEqual(['compare-group-capacity', 'compare-bandwidth', 'compare-disk', 'compare-group-features', 'compare-ai_seo', 'compare-group-access', 'compare-gitea_iac', 'compare-group-ops', 'compare-vuln_dashboard', 'compare-group-scope', 'compare-dedicated_ip', 'compare-group-resilience', 'compare-backup', 'compare-dr_topology', 'step-up-hints'])
    expect(t.querySelector('[data-testid=compare-group-ops] th')?.textContent).toBe('Managed operations')
    // A quantity with what happens above it.
    const bw = [...t.querySelectorAll('[data-testid=compare-bandwidth] td')].map((c) => c.textContent)
    expect(bw.slice(1)).toEqual(['50 Mbpshard cap', '100 Mbpshard cap', '250 Mbpsmore billed per use', '1000 Mbpsmore billed per use'])
    // A boolean add-on: one line "+ price / mo" (the currency is the column
    // header's and the tooltip's) over its hint; included on XL.
    const backup = [...t.querySelectorAll('[data-testid=compare-backup] td')].map((c) => c.textContent)
    expect(backup[0]).toBe('Backup')
    expect(t.querySelector('[data-testid=compare-backup] td')?.getAttribute('title')).toBe('Scheduled backups of your sites and databases')
    expect(backup[1]).toBe('+ 1.500 / moincluded from XL')
    expect(t.querySelector('[data-testid=compare-backup] td:nth-child(2) label')?.getAttribute('title')).toBe('1.500 OMR a month')
    expect(backup[4]).toBe('✓ Included')
    const ip = [...t.querySelectorAll('[data-testid=compare-dedicated_ip] td')].map((c) => c.textContent)
    expect(ip[3]).toBe('—')
    expect(ip[4]).toBe('+ 2.000 / mo')
    // An access door: ✓ / —, with its note.
    const gitea = [...t.querySelectorAll('[data-testid=compare-gitea_iac] td')].map((c) => c.textContent)
    expect(gitea.slice(1)).toEqual(['—', '✓read', '✓', '✓'])
    // A teaser says where it starts instead of a dash.
    const vuln = [...t.querySelectorAll('[data-testid=compare-vuln_dashboard] td')].map((c) => c.textContent)
    expect(vuln.slice(1)).toEqual(['from M', '✓ Included', '✓ Included', '✓ Included'])
    // A level shows its label; the purchasable next level is a tick with its price.
    const dr = [...t.querySelectorAll('[data-testid=compare-dr_topology] td')].map((c) => c.textContent)
    expect(dr.slice(1)).toEqual(['single region+ 8.000 / moactive-passive', 'active-passive', 'active-passive', 'active-passive'])
    // The floor, once, under the table — outside the table's scroll
    // container, like the footer, so both stay the card's full width when
    // the table scrolls on a narrow screen (0.1.62).
    const strip = document.querySelector('[data-testid=floor-strip]')
    expect(strip?.textContent).toBe('On every package: Unlimited free SSL · Standard DDoS protection')
    const scroll = t.querySelector('[data-testid=package-table-scroll]')
    expect(scroll?.querySelector('table.pkg-table.compare')).not.toBeNull()
    expect(scroll?.querySelectorAll('colgroup col')).toHaveLength(5)
    expect(scroll?.contains(strip)).toBe(false)
    expect(scroll?.contains(byAria('Configure Platform plans'))).toBe(false)
    // No step-up hint until something is ticked.
    expect(document.querySelector('[data-testid=step-up-hint-plan\\.s]')).toBeNull()
  })

  it('hints the step up when the ticked add-ons are worth it, and the button chooses the next package', async () => {
    await mount()
    await click(byAria('DR topology — active-passive on S'))
    const hint = document.querySelector('[data-testid=step-up-hint-plan\\.s]')
    expect(hint).not.toBeNull()
    expect(hint!.textContent).toBe('M includes all of this for 4.000 OMR moreChoose M instead')
    // Backup on S is not included on M: the hint goes.
    await click(byAria('Backup on S'))
    expect(document.querySelector('[data-testid=step-up-hint-plan\\.s]')).toBeNull()
    await click(byAria('Backup on S'))
    await click(byAria('Choose M instead of S'))
    const group = summary().querySelector('[data-testid=group-plan]')!
    expect(group).not.toBeNull()
    const lines = [...group.querySelectorAll('[data-testid=item-lines] .row')].map((l) => l.textContent)
    expect(lines).toHaveLength(1)
    expect(lines[0]).toContain('plan.m')
    expect(group.textContent).not.toContain('DR topology')
  })

  it('choosing M with Backup ticked adds the plan line and the add-on line, priced by the month, with the hint beside the add-on', async () => {
    await mount()
    const tick = byAria('Backup on M') as HTMLInputElement
    expect(tick.checked).toBe(false)
    await click(tick)
    expect(tick.checked).toBe(true)
    // The hint sits in the cell the tick is in.
    expect(tick.closest('td')?.textContent).toContain('included from XL')

    await click(byAria('Choose M'))
    const group = summary().querySelector('[data-testid=group-plan]')!
    expect(group).not.toBeNull()
    expect(group.textContent).toContain('Platform plans › Platform plans')
    expect(group.textContent).toContain('1 × M plan · 4 vCPU · 8 GB · 1 month(s) + Backup')
    const lines = [...group.querySelectorAll('[data-testid=item-lines] .row')].map((l) => l.textContent)
    expect(lines).toHaveLength(2)
    expect(lines[0]).toContain('M plan · 4 vCPU · 8 GB')
    expect(lines[0]).toContain('plan.m')
    expect(lines[0]).toContain('1 × 1 month(s)')
    expect(lines[1]).toContain('Backup add-on')
    expect(lines[1]).toContain('addon.backup')
    expect(lines[1]).toContain('1 × 1 month(s)')

    await priced()
    expect(posts.map((p) => p.path)).toEqual(['/public/estimates?preview=1'])
    expect(posts[0].body).toEqual({
      lines: [
        { plan: 'm', quantity: '1', months: 1 },
        { sku: 'addon.backup', quantity: '1', months: 1 },
      ],
    })
    // 730 × 0.01232877 = 9.000002; 730 × 0.00205479 = 1.499997 → 10.499999.
    expect(group.querySelector('[data-testid=item-amount]')?.textContent).toBe('10.500 OMR')
    const priceOf = [...group.querySelectorAll('[data-testid=item-lines] .row .num')].map((l) => l.textContent)
    expect(priceOf).toEqual(['9.000 OMR', '1.500 OMR'])
    expect(summary().querySelector('[data-testid=total-monthly]')?.textContent).toBe('11.025 OMR')

    // Edit re-opens the plan configurator with the add-on ticked and the hint; unticking drops the line.
    await click(byAria('Edit Platform plans'))
    expect(dialog()).not.toBeNull()
    const addonTick = byAria('Backup add-on') as HTMLInputElement
    expect(addonTick.checked).toBe(true)
    expect(dialog()!.querySelector('[data-testid=plan-addons]')?.textContent).toContain('included from XL')
    expect(dialog()!.querySelectorAll('[data-testid=configurator-line]')).toHaveLength(2)
    await click(addonTick)
    expect(dialog()!.querySelectorAll('[data-testid=configurator-line]')).toHaveLength(1)
    await click([...document.querySelectorAll('button')].find((b) => b.textContent === 'Save changes')!)
    await priced()
    expect(posts.at(-1)?.body).toEqual({ lines: [{ plan: 'm', quantity: '1', months: 1 }] })
    expect(summary().textContent).not.toContain('Backup add-on')
    expect(summary().textContent).not.toMatch(/NaN|undefined/)
  })

  it('choosing XL adds no Backup line even when ticked elsewhere — the package includes it', async () => {
    await mount()
    // Backup is included in XL: there is no tick box in that column.
    expect(document.querySelector('[aria-label="Backup on XL"]')).toBeNull()
    await click(byAria('Dedicated IP address on XL'))
    await click(byAria('Choose XL'))
    const group = summary().querySelector('[data-testid=group-plan]')!
    const lines = [...group.querySelectorAll('[data-testid=item-lines] .row')].map((l) => l.textContent)
    expect(lines).toHaveLength(2)
    expect(lines[0]).toContain('plan.xl')
    expect(lines[1]).toContain('Dedicated IP address add-on')
    expect(group.textContent).not.toContain('Backup')
  })
})
