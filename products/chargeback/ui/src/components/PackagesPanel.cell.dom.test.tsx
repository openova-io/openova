// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { packages } from '../panels/estimate/fixture'

/**
 * Saving a cell from the Packages tab, walked in a browser document
 * (DESIGN.md §22.5): a quantity cell opened from its chip, the quantity and
 * the overage changed, saved — the PUT the server receives carries the state,
 * the quantity, the overage and the note; a level cell saved with the next
 * level ticked and priced carries the level, the optional state and the
 * add-on price; the panel reloads and says what it did.
 */

;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const features = {
  features: [
    { id: 'f1', key: 'bandwidth', name: 'Bandwidth', blurb: '', kind: 'quantity', group: 'capacity', unit: 'Mbps', addon_sku: 'eip.bandwidth_mbps', teaser: false, sort_order: 1 },
    { id: 'f8', key: 'dr_topology', name: 'DR topology', blurb: '', kind: 'level', group: 'resilience', levels: ['single region', 'active-passive'], addon_sku: 'addon.dr', teaser: false, sort_order: 8 },
  ],
  groups: packages.groups,
}
const reloads = { n: 0 }

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => ({
    data: path === '/pricebooks/pb1/packages' ? packages : path === '/features' ? features : null,
    error: '',
    loading: false,
    reload: async () => {
      reloads.n++
    },
    setData: () => {},
  }),
}))

const puts: Array<{ path: string; body: unknown }> = []

vi.mock('../api/client', () => ({
  api: {
    put: async (path: string, body: unknown) => {
      puts.push({ path, body })
      return {}
    },
    del: async () => ({}),
    post: async () => ({}),
    patch: async () => ({}),
  },
  asList: (data: unknown, key: string) => ((data as Record<string, unknown[]>)?.[key] ?? []) as unknown[],
  errorText: (e: unknown) => String(e),
}))

import { PackagesPanel } from './PackagesPanel'

const book = { id: 'pb1', name: 'OpenOva plans', scope: 'platform', currency: 'OMR', annual_divisor: 8760, bill_stopped: 'compute', created_at: '2026-01-01T00:00:00Z', items: [] } as never

let host: HTMLDivElement
let root: Root | undefined

beforeEach(() => {
  puts.length = 0
  reloads.n = 0
  host = document.createElement('div')
  document.body.appendChild(host)
})

afterEach(async () => {
  await act(async () => root?.unmount())
  host.remove()
})

async function mount() {
  await act(async () => {
    root = createRoot(host)
    root.render(<PackagesPanel book={book} canManage />)
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

async function type(el: HTMLInputElement | HTMLSelectElement, value: string) {
  await act(async () => {
    const proto = el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype
    const setter = Object.getOwnPropertyDescriptor(proto, 'value')!.set!
    setter.call(el, value)
    el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }))
  })
}

async function submit() {
  const form = document.querySelector('form#package-cell-form') as HTMLFormElement
  await act(async () => {
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  })
  await act(async () => {
    await Promise.resolve()
  })
}

describe('saving a cell', () => {
  it('writes a quantity cell with its quantity, overage and note', async () => {
    await mount()
    await click(byAria('Bandwidth on S'))
    const dialog = document.querySelector('[role=dialog]')
    expect(dialog).not.toBeNull()
    expect(dialog!.querySelector('[data-testid=cell-editor-quantity]')).not.toBeNull()
    await type(byAria('Included quantity') as HTMLInputElement, '60')
    await type(byAria('Overage') as HTMLSelectElement, 'metered')
    await type(byAria('Note') as HTMLInputElement, 'burst allowed')
    await submit()
    expect(puts).toEqual([{ path: '/pricebooks/pb1/packages/plan.s/features/bandwidth', body: { state: 'included', note: 'burst allowed', included_quantity: '60', overage: 'metered' } }])
    expect(reloads.n).toBeGreaterThan(0)
    expect(document.querySelector('[role=dialog]')).toBeNull()
    expect(host.textContent).toContain('Bandwidth on S: included')
  })

  it('writes a level cell at its level with the next level purchasable and priced', async () => {
    await mount()
    await click(byAria('DR topology on M'))
    const dialog = document.querySelector('[role=dialog]')!
    expect(dialog.querySelector('[data-testid=cell-editor-level]')).not.toBeNull()
    // M is at active-passive, the top: move it down to single region and offer the next level.
    await type(byAria('Level') as HTMLSelectElement, '0')
    await click(byAria('Next level purchasable'))
    await type(document.querySelector('form#package-cell-form input[type=number]') as HTMLInputElement, '8.000')
    await submit()
    expect(puts).toEqual([{ path: '/pricebooks/pb1/packages/plan.m/features/dr_topology', body: { state: 'optional', note: '', level: 0, addon_monthly: '8.000', grow_only: false } }])
    expect(host.textContent).toContain('DR topology on M: single region, active-passive purchasable')
  })

  it('writes a level cell whose next level comes with grow mode only: optional, grow_only, no price (DESIGN.md §22.11)', async () => {
    await mount()
    await click(byAria('DR topology on M'))
    await type(byAria('Level') as HTMLSelectElement, '0')
    await click(byAria('Next level in grow mode only'))
    expect((byAria('Next level purchasable') as HTMLInputElement).disabled).toBe(true)
    await submit()
    expect(puts).toEqual([{ path: '/pricebooks/pb1/packages/plan.m/features/dr_topology', body: { state: 'optional', note: '', level: 0, grow_only: true } }])
    expect(host.textContent).toContain('DR topology on M: single region, active-passive in grow mode only')
  })
})
