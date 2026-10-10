// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CostSource, PackageInfo, PackagesDoc } from '../api/types'
import { packages } from './estimate/fixture'

/**
 * Capped or grow on the console (DESIGN.md §22.11), walked in a browser
 * document: the Sources tab names a platform source's overage mode; the
 * Overage modal writes capped, or grow with a ceiling and a spend limit, and
 * refuses a ceiling outside the package's range before sending; the package
 * settings modal keeps grow in its whole write.
 */

;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const growM: PackageInfo = {
  ...(packages.packages.find((p) => p.sku === 'plan.m') as PackageInfo),
  includes: { vcpu: 2, memory_gb: 4, disk_gb: 50, bandwidth_mbps: 100 },
  grow: {
    allowed: true,
    ceiling: { vcpu: 8, memory_gb: 16, disk_gb: 250, bandwidth_mbps: 1000 },
    overage_rates: [
      { key: 'vcpu', sku: 'k8s.vcpu', unit: 'vCPU', price_month: '1.796' },
      { key: 'memory', sku: 'k8s.mem_gb', unit: 'GB', price_month: '0.337' },
      { key: 'disk', sku: 'k8s.pvc_gb', unit: 'GB', price_month: '0.035' },
      { key: 'bandwidth', sku: 'eip.bandwidth_mbps', unit: 'Mbps', price_month: '1.253' },
    ],
  },
}
const doc: PackagesDoc = { ...packages, packages: packages.packages.map((p) => (p.sku === 'plan.m' ? growM : p)) }

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => ({ data: path === '/pricebooks/plans/packages' ? doc : null, error: '', loading: false, reload: async () => {}, setData: () => {} }),
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

import { OverageModal, SourcesPanel, overageText } from './SourcesPanel'
import { PackageSettingsModal } from '../components/PackagesPanel'

const capped: CostSource = { id: 's1', customer_id: 'c1', kind: 'openova-org', layer: 'platform', price_book_id: 'plans', region: '', project_id: 'sohar', status: 'verified', overage_mode: 'capped' }
const grown: CostSource = { ...capped, id: 's2', project_id: 'sur', overage_mode: 'grow', grow_ceiling: { vcpu: 4, memory_gb: 16, disk_gb: 250, bandwidth_mbps: 1000 }, spend_limit_month: 25 }

let host: HTMLDivElement
let root: Root | undefined
beforeEach(() => {
  puts.length = 0
  host = document.createElement('div')
  document.body.appendChild(host)
})
afterEach(async () => {
  await act(async () => root?.unmount())
  host.remove()
})
async function mount(el: React.ReactElement) {
  await act(async () => {
    root = createRoot(host)
    root.render(el)
  })
}
function input(label: string): HTMLInputElement {
  const el = host.querySelector(`[aria-label="${label}"]`) as HTMLInputElement | null
  if (!el) throw new Error(`no ${label}`)
  return el
}
async function type(label: string, value: string) {
  const el = input(label)
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    setter.call(el, value)
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}
async function click(el: Element) {
  await act(async () => {
    ;(el as HTMLElement).click()
  })
}
function button(text: string): HTMLButtonElement {
  const b = Array.from(host.querySelectorAll('button')).find((x) => x.textContent?.trim() === text)
  if (!b) throw new Error(`no button ${text}`)
  return b as HTMLButtonElement
}

describe('the overage mode', () => {
  it('names capped and grow in words', () => {
    expect(overageText(capped)).toBe('capped')
    expect(overageText({})).toBe('capped')
    expect(overageText(grown, 'OMR')).toBe('grow · up to 4 vCPU / 16 GB / 250 GB disk / 1000 Mbps · usage capped at 25 OMR / month')
  })

  it('shows each platform source’s mode with a Change button', async () => {
    await mount(<SourcesPanel customerId="c1" sources={[capped, grown]} books={[]} canManage canRotate onChanged={() => {}} planSlug="m" />)
    expect(host.querySelector('[data-testid="overage-s1"]')?.textContent).toBe('capped')
    expect(host.querySelector('[data-testid="overage-s2"]')?.textContent).toContain('grow · up to 4 vCPU')
  })

  it('writes grow with a ceiling and a spend limit, and refuses a ceiling above the package’s', async () => {
    await mount(<OverageModal customerId="c1" source={capped} planSlug="m" onClose={() => {}} onDone={() => {}} />)
    await click(input('Grow'))
    expect(host.querySelector('[data-testid="overage-rates"]')?.textContent).toContain('1.796 OMR per vCPU / month')
    await type('Ceiling vCPU', '9')
    expect(host.textContent).toContain('vCPU: at most the package’s ceiling, 8')
    expect(button('Save').disabled).toBe(true)
    await type('Ceiling vCPU', '1')
    expect(host.textContent).toContain('vCPU: at least the package’s 2')
    await type('Ceiling vCPU', '4')
    await type('Spend limit', '25.000')
    await click(button('Save'))
    expect(puts).toEqual([{ path: '/customers/c1/sources/s1/overage', body: { overage_mode: 'grow', grow_ceiling: { vcpu: '4' }, spend_limit_month: '25.000' } }])
  })

  it('writes capped with nothing else', async () => {
    await mount(<OverageModal customerId="c1" source={grown} planSlug="m" onClose={() => {}} onDone={() => {}} />)
    await click(input('Capped'))
    await click(button('Save'))
    expect(puts).toEqual([{ path: '/customers/c1/sources/s2/overage', body: { overage_mode: 'capped' } }])
  })

  it('keeps grow in the package settings’ whole write', async () => {
    const book = { id: 'plans', name: 'OpenOva plans', scope: 'platform', currency: 'OMR', annual_divisor: 8760, bill_stopped: 'compute', created_at: '2026-01-01T00:00:00Z', items: [] } as never
    await mount(<PackageSettingsModal book={book} pkg={growM} currency="OMR" onClose={() => {}} onSaved={() => {}} />)
    expect(input('Grow allowed').checked).toBe(true)
    expect(input('Overage vCPU per month').value).toBe('1.796')
    await click(button('Save'))
    const body = puts[0].body as Record<string, unknown>
    expect(body).toMatchObject({ grow_allowed: true, grow_ceiling_vcpu: '8', grow_ceiling_memory_gb: '16', grow_ceiling_disk_gb: '250', grow_ceiling_bandwidth_mbps: '1000', overage_vcpu_month: '1.796', overage_mem_gb_month: '0.337' })
  })
})
