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
  it('draws S / M / L / XL with their prices, the included quantities and the three states', async () => {
    await mount()
    const t = table()
    expect(t).not.toBeNull()
    // The plans family is the table, not a Configure row.
    expect(document.querySelector('[data-testid=family-plans] .strip-row')).toBeNull()
    const heads = [...t.querySelectorAll('th.pkg-head')].map((h) => h.textContent)
    expect(heads).toEqual(['S5.000OMR / monthChoose', 'M9.000OMR / monthChoose', 'L16.000OMR / monthChoose', 'XL30.000OMR / monthChoose'])
    expect([...t.querySelectorAll('th.pkg-head button')].map((b) => b.getAttribute('aria-label'))).toEqual(['Choose S', 'Choose M', 'Choose L', 'Choose XL'])
    expect([...t.querySelectorAll('[data-testid=includes-vcpu] td')].map((c) => c.textContent)).toEqual(['vCPU', '2', '4', '8', '16'])
    expect([...t.querySelectorAll('[data-testid=includes-bandwidth_mbps] td')].map((c) => c.textContent)).toEqual(['Bandwidth (Mbps)', '50', '100', '250', '1000'])
    const ssl = [...t.querySelectorAll('[data-testid=compare-ssl] td')].map((c) => c.textContent)
    expect(ssl.slice(1)).toEqual(['✓ Included', '✓ Included', '✓ Included', '✓ Included'])
    const backup = [...t.querySelectorAll('[data-testid=compare-backup] td')].map((c) => c.textContent)
    expect(backup[0]).toBe('Backup')
    expect(t.querySelector('[data-testid=compare-backup] td')?.getAttribute('title')).toBe('Daily backups of your sites and databases, kept 30 days')
    expect(backup[1]).toBe('+ 1.500OMR / monthincluded from XL')
    expect(backup[4]).toBe('✓ Included')
    const ip = [...t.querySelectorAll('[data-testid=compare-dedicated_ip] td')].map((c) => c.textContent)
    expect(ip[3]).toBe('—')
    expect(ip[4]).toBe('+ 2.000OMR / month')
    const bw = [...t.querySelectorAll('[data-testid=compare-bandwidth] td')].map((c) => c.textContent)
    expect(bw.slice(1)).toEqual(['50 Mbps', '100 Mbps', '250 Mbps', '1000 Mbps'])
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
