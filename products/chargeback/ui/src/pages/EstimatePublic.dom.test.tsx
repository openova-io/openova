// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Estimate } from '../api/types'
import { catalog, unitPrices } from '../panels/estimate/fixture'

/**
 * The public calculator walked in a browser document (DESIGN.md §12.5):
 * open the Elastic Cloud Server configurator, choose a family, a size, a
 * quantity, the usage and a disk, add it, and read the estimate back —
 * the grouped item, its lines, the figures the server returned. Then edit
 * it (the configurator re-opens with the same values), and remove it
 * (the empty state returns). The server is a fake that prices each line
 * as quantity × hours × unit price; the page shows what it answers.
 */

;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => ({ data: path === '/public/catalog' ? catalog : null, error: '', loading: false, reload: async () => {}, setData: () => {} }),
}))

const posts: Array<{ path: string; body: unknown }> = []

vi.mock('../api/client', () => ({
  api: {
    post: async (path: string, body: { lines: Array<{ sku?: string; plan?: string; quantity: string; hours_per_month?: string; months?: number }> }) => {
      posts.push({ path, body })
      // The term is every line whole; the month is each line over its
      // months — the rule DESIGN.md §12.2 gives the real server.
      let subtotal = 0
      let monthlySub = 0
      const lines = body.lines.map((l) => {
        const sku = l.plan ? `plan.${l.plan}` : (l.sku ?? '')
        const months = l.months ?? 1
        const hours = l.plan ? 730 * months : Number(l.hours_per_month ?? 730)
        const rated = Number(l.quantity) * hours
        const amount = rated * unitPrices[sku]
        subtotal += amount
        monthlySub += amount / months
        return { sku, plan: l.plan, unit: 'x', quantity: l.quantity, hours: String(hours), months, rated_quantity: rated.toFixed(6), unit_price: unitPrices[sku].toFixed(8), amount: amount.toFixed(6) }
      })
      const tax = subtotal * 0.05
      const total = subtotal + tax
      const monthly = monthlySub * 1.05
      const e: Estimate = {
        id: path.includes('preview') ? '' : 'e9',
        lines,
        currency: 'OMR',
        subtotal: subtotal.toFixed(6),
        tax_rate: '0.0500',
        tax: tax.toFixed(6),
        total: total.toFixed(6),
        monthly: monthly.toFixed(6),
        yearly: (monthly * 12).toFixed(6),
        price_book: catalog.price_book,
        list_prices: true,
        lead: false,
        created_at: '2026-09-11T10:00:00Z',
        valid_until: '2026-10-11T10:00:00Z',
        share_url: path.includes('preview') ? undefined : 'https://billing.t99.omani.works/estimate/e9',
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

/** The control a label names, the way a person finds it. */
function byLabel(text: string): HTMLInputElement | HTMLSelectElement {
  const label = [...document.querySelectorAll('label')].find((l) => l.textContent?.trim() === text)
  if (!label?.htmlFor) throw new Error(`no labelled control "${text}"`)
  return document.getElementById(label.htmlFor) as HTMLInputElement | HTMLSelectElement
}

function button(text: string): HTMLButtonElement {
  const b = [...document.querySelectorAll('button')].find((x) => x.textContent?.trim() === text || x.getAttribute('aria-label') === text)
  if (!b) throw new Error(`no button "${text}"`)
  return b
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
}

async function setValue(el: HTMLInputElement | HTMLSelectElement, value: string) {
  const proto = el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype
  Object.getOwnPropertyDescriptor(proto, 'value')?.set?.call(el, value)
  await act(async () => el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true })))
}

/** Lets the debounced preview fire and its answer land. */
async function priced() {
  await act(async () => {
    vi.advanceTimersByTime(300)
  })
  await act(async () => {
    await Promise.resolve()
  })
}

const dialog = () => document.querySelector('[role=dialog]')
const summary = () => document.querySelector('[data-testid=estimate-summary]') as HTMLElement
const sizeChips = () => [...(dialog()?.querySelectorAll('[role=radio]') ?? [])]
const chosenSize = () => dialog()?.querySelector('[role=radio][aria-checked=true]')?.getAttribute('aria-label')

describe('the public calculator, walked', () => {
  it('opens the Elastic Cloud Server configurator, adds a configured server, and shows it grouped with its lines and the server’s figures', async () => {
    await mount()
    expect(summary().textContent).toContain('Choose a service on the left to start an estimate')
    expect(dialog()).toBeNull()

    await click(button('Configure Elastic Cloud Server'))
    expect(dialog()).not.toBeNull()
    expect(dialog()!.querySelector('h2')?.textContent).toBe('Elastic Cloud Server')
    // Every choice is a dropdown, a chip or a number; the default is the smallest server.
    expect((byLabel('Family') as HTMLSelectElement).value).toBe('general')
    expect(sizeChips().map((c) => c.getAttribute('aria-label'))).toEqual(['1 vCPU · 1 GB', '4 vCPU · 16 GB'])
    expect(sizeChips()[0].textContent).toBe('1 vCPU · 1 GB11.676 OMR / month')
    expect(chosenSize()).toBe('1 vCPU · 1 GB')

    await setValue(byLabel('Family'), 'compute')
    expect(sizeChips().map((c) => c.getAttribute('aria-label'))).toEqual(['2 vCPU · 4 GB', '8 vCPU · 16 GB'])
    expect(chosenSize()).toBe('2 vCPU · 4 GB')
    await click(button('8 vCPU · 16 GB'))
    expect(chosenSize()).toBe('8 vCPU · 16 GB')
    await setValue(byLabel('Servers'), '2')
    await setValue(byLabel('Usage'), 'business')
    await setValue(byLabel('Attached disk'), 'ssd')
    await setValue(byLabel('Disk size (GB) per server'), '100')
    const preview = [...dialog()!.querySelectorAll('[data-testid=configurator-line]')].map((l) => l.textContent)
    expect(preview).toHaveLength(2)
    expect(preview[0]).toContain('Compute-optimised · 8 vCPU · 16 GB')
    expect(preview[0]).toContain('ecs.c7n.2xlarge.2')
    expect(preview[0]).toContain('2 × 176 h')
    expect(preview[1]).toContain('SSD disk · 100 GB each')
    expect(preview[1]).toContain('200 × 730 h')

    await click(button('Add to estimate'))
    expect(dialog()).toBeNull()
    const group = summary().querySelector('[data-testid=group-ecs]')!
    expect(group.textContent).toContain('Compute › Elastic Cloud Server')
    expect(group.textContent).toContain('2 × Compute-optimised · 8 vCPU · 16 GB · business hours')
    // The new item opens on its lines, each with the SKU as a small hint.
    const lines = [...group.querySelectorAll('[data-testid=item-lines] .row')].map((l) => l.textContent)
    expect(lines).toHaveLength(2)
    expect(lines[0]).toContain('Compute-optimised · 8 vCPU · 16 GB')
    expect(lines[0]).toContain('ecs.c7n.2xlarge.2')
    expect(lines[1]).toContain('SSD disk · 100 GB each')
    expect(lines[1]).toContain('evs.ssd.gb')
    // Nothing is priced until the server answers.
    expect(group.querySelector('[data-testid=item-amount]')?.textContent).toBe('—')

    await priced()
    expect(posts.map((p) => p.path)).toEqual(['/public/estimates?preview=1'])
    expect(posts[0].body).toEqual({
      lines: [
        { sku: 'ecs.c7n.2xlarge.2', quantity: '2', hours_per_month: '176' },
        { sku: 'evs.ssd.gb', quantity: '200', hours_per_month: '730' },
      ],
    })
    // 2 × 176 h × 0.19495082 = 68.622689; 200 GB × 730 h × 0.00022831 = 33.333260.
    expect(group.querySelector('[data-testid=item-amount]')?.textContent).toBe('101.956 OMR')
    const priceOf = [...group.querySelectorAll('[data-testid=item-lines] .row .num')].map((l) => l.textContent)
    expect(priceOf).toEqual(['68.623 OMR', '33.333 OMR'])
    expect(summary().querySelector('[data-testid=total-monthly]')?.textContent).toBe('107.054 OMR')
    expect(summary().querySelector('[data-testid=total-yearly]')?.textContent).toBe('1,284.645 OMR')
    expect(summary().textContent).toContain('1 item · 2 lines · OMR')
    // One family so far: the gauge is full and the bar is one segment.
    const breakdown = summary().querySelector('[data-testid=breakdown]')!
    expect(breakdown.querySelector('.gauge')?.getAttribute('aria-label')).toBe('Compute is 100 % of the estimate')
    expect([...breakdown.querySelectorAll('[data-testid=breakdown-family]')].map((f) => f.textContent)).toEqual(['Compute 101.956 OMR 100 %'])
    expect(summary().textContent).not.toMatch(/NaN|undefined/)
  })

  it('re-opens the configurator with the item’s values on Edit, saves the change in place, and returns to the empty state on Remove', async () => {
    await mount()
    await click(button('Configure Elastic Cloud Server'))
    await setValue(byLabel('Family'), 'memory')
    await setValue(byLabel('Servers'), '3')
    await setValue(byLabel('Usage'), 'custom')
    await setValue(byLabel('Hours per month'), '300')
    await click(button('Add to estimate'))
    await priced()
    expect(summary().textContent).toContain('3 × Memory-optimised · 2 vCPU · 16 GB · 300 h/month')

    await click(button('Edit Elastic Cloud Server'))
    expect(dialog()!.querySelector('h2')?.textContent).toBe('Edit Elastic Cloud Server')
    expect((byLabel('Family') as HTMLSelectElement).value).toBe('memory')
    expect(chosenSize()).toBe('2 vCPU · 16 GB')
    expect((byLabel('Servers') as HTMLInputElement).value).toBe('3')
    expect((byLabel('Usage') as HTMLSelectElement).value).toBe('custom')
    expect((byLabel('Hours per month') as HTMLInputElement).value).toBe('300')
    expect((byLabel('Attached disk') as HTMLSelectElement).value).toBe('')

    // A bad value disables the save and names the rule; a good one saves in place.
    await setValue(byLabel('Hours per month'), '745')
    expect(button('Save changes').disabled).toBe(true)
    expect(dialog()!.textContent).toContain('hours must be more than 0 and at most 744')
    await setValue(byLabel('Hours per month'), '200')
    await setValue(byLabel('Servers'), '4')
    await click(button('Save changes'))
    expect(dialog()).toBeNull()
    expect(summary().querySelectorAll('[data-testid^=item-i]')).toHaveLength(1)
    expect(summary().textContent).toContain('4 × Memory-optimised · 2 vCPU · 16 GB · 200 h/month')
    await priced()
    expect(posts.at(-1)?.body).toEqual({ lines: [{ sku: 'ecs.m7n.large.8', quantity: '4', hours_per_month: '200' }] })
    // 4 × 200 h × 0.07829879 = 62.639032
    expect(summary().querySelector('[data-testid=item-amount]')?.textContent).toBe('62.639 OMR')

    await click(button('Remove Elastic Cloud Server'))
    expect(summary().textContent).toContain('Choose a service on the left to start an estimate')
    expect(summary().querySelector('[data-testid=total-monthly]')?.textContent).toBe('—')
    expect(summary().querySelector('[data-testid=breakdown]')).toBeNull()
    expect(summary().querySelectorAll('[data-testid^=item-i]')).toHaveLength(0)
  })

  it('groups a second service under its own heading and asks a database for its deployment, size and storage', async () => {
    await mount()
    await click(button('Configure RDS for MySQL'))
    expect((byLabel('Deployment') as HTMLSelectElement).value).toBe('single')
    await setValue(byLabel('Deployment'), 'ha')
    expect(sizeChips().map((c) => c.getAttribute('aria-label'))).toEqual(['2 vCPU · 4 GB', '4 vCPU · 16 GB'])
    await click(button('4 vCPU · 16 GB'))
    await setValue(byLabel('Storage (GB) per database'), '250')
    await click(button('Add to estimate'))
    await click(button('Configure Platform plans'))
    await setValue(byLabel('Plan'), 'm')
    await setValue(byLabel('Months'), '3')
    await click(button('Add to estimate'))
    await priced()
    const groups = [...summary().querySelectorAll('[data-testid^=group-]')].map((g) => g.getAttribute('data-testid'))
    expect(groups).toEqual(['group-rds-mysql', 'group-plan'])
    expect(summary().textContent).toContain('Databases › RDS for MySQL')
    expect(summary().textContent).toContain('1 × 4 vCPU · 16 GB · Primary + standby · 250 GB · always on')
    expect(summary().textContent).toContain('Platform plans › Platform plans')
    expect(summary().textContent).toContain('1 × M plan · 4 vCPU · 8 GB · 3 month(s)')
    expect(posts.at(-1)?.body).toEqual({
      lines: [
        { sku: 'rds.mysql.c7.xlarge.4.ha', quantity: '1', hours_per_month: '730' },
        { sku: 'rds.storage.ha.gb', quantity: '250', hours_per_month: '730' },
        { plan: 'm', quantity: '1', months: 3 },
      ],
    })
    // A plan taken for three months shows its term, not a month.
    const amounts = [...summary().querySelectorAll('[data-testid=item-amount]')].map((a) => a.parentElement?.textContent)
    expect(amounts[0]).toContain('per month · before tax')
    expect(amounts[1]).toContain('for the term · before tax')
    expect(summary().textContent).toContain('Total for the term')
    // Two families: the bar has two segments in proportion, the gauge shows the larger.
    // Databases 378.478 of 405.478 = 93 %.
    const breakdown = summary().querySelector('[data-testid=breakdown]')!
    expect([...breakdown.querySelectorAll('[data-testid=breakdown-family]')].map((f) => f.textContent)).toEqual(['Databases 378.478 OMR 93 %', 'Platform plans 27.000 OMR 7 %'])
    expect(breakdown.querySelector('.gauge')?.getAttribute('aria-label')).toBe('Databases is 93 % of the estimate')
    const widths = [...breakdown.querySelectorAll('.breakdown-bar i')].map((i) => (i as HTMLElement).style.width)
    expect(widths).toEqual(['93.34%', '6.66%'])
  })
})
