// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Contract, ContractPeriods, Me } from '../api/types'

/**
 * The contract's rated periods walked in a browser document (DESIGN.md
 * §15.10): the newest period is open on arrival; opening an older one
 * shows its consumption and closes the newest; closing it leaves none open.
 * A render test proves the markup; this proves the click.
 */

;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const contract: Contract = {
  id: 'ct1',
  customer_id: 'c1',
  customer_name: 'ACME LLC',
  name: 'ACME 2026',
  starts_on: '2026-01-01',
  ends_on: '2026-12-31',
  term_months: 12,
  auto_renew: true,
  renewal_notice_days: 30,
  minimum_commitment: '600',
  currency: 'OMR',
  status: 'active',
  renewal_date: '2027-01-01',
  notice_from: '2026-12-01',
  items: [{ id: 'i1', kind: 'commitment', sku: 'ecs.m7n.2xlarge.8', unit: 'instance-hour', quantity: '1500', committed_price: '0.30' }],
}

const periods: ContractPeriods = {
  contract_id: 'ct1',
  currency: 'OMR',
  floor: '600',
  periods: [
    {
      period: '2026-08',
      period_start: '2026-08-01',
      period_end: '2026-08-31',
      statement_id: 'st-aug',
      status: 'draft',
      currency: 'OMR',
      subtotal: '600',
      discount_total: '0',
      total: '630',
      net: '350',
      true_up: '250',
      floor: '600',
      allowances: [],
      commitments: [{ sku: 'ecs.m7n.2xlarge.8', unit: 'instance-hour', quantity: '1000', committed: '1500', delivered: '1000', shortfall: '500', excess: '0', committed_price: '0.30', amount: '300' }],
      discounts: [],
    },
    {
      period: '2026-07',
      period_start: '2026-07-01',
      period_end: '2026-07-31',
      statement_id: 'st-jul',
      status: 'paid',
      currency: 'OMR',
      subtotal: '1100',
      discount_total: '0',
      total: '1155',
      net: '1100',
      true_up: '0',
      floor: '600',
      allowances: [],
      commitments: [{ sku: 'ecs.m7n.2xlarge.8', unit: 'instance-hour', quantity: '5000', committed: '1500', delivered: '1500', shortfall: '0', excess: '3500', committed_price: '0.30', amount: '2200' }],
      discounts: [],
    },
  ],
}

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => ({
    data: path === '/contracts/ct1' ? contract : path === '/contracts/ct1/periods' ? periods : null,
    error: '',
    loading: false,
    reload: async () => {},
    setData: () => {},
  }),
}))

const me: Me = {
  email: 'fin@nc.example',
  role: 'finance-viewer',
  roles: [{ role: 'finance-viewer', scope_kind: 'sovereign' }],
  permissions: { sovereign: ['metering.read'] },
  scopes: ['sovereign'],
}
vi.mock('../auth/session', () => ({
  useSession: () => ({ me, loading: false, logout: async () => {}, reload: async () => {} }),
  SessionProvider: ({ children }: { children: unknown }) => children,
}))

import { ContractDetail } from './ContractDetail'

let root: Root | null = null
let host: HTMLElement | null = null

beforeEach(() => {
  host = document.createElement('div')
  document.body.appendChild(host)
})

afterEach(() => {
  act(() => root?.unmount())
  host?.remove()
  root = null
  host = null
})

function mount() {
  act(() => {
    root = createRoot(host!)
    root.render(
      <MemoryRouter initialEntries={['/contracts/ct1']}>
        <Routes>
          <Route path="/contracts/:id" element={<ContractDetail />} />
        </Routes>
      </MemoryRouter>,
    )
  })
}

const detail = (period: string) => host!.querySelector(`[data-period-detail="${period}"]`)
const toggle = (period: string) => host!.querySelector<HTMLButtonElement>(`[data-period="${period}"] button[aria-expanded]`)!

describe('the rated periods, clicked', () => {
  it('opens the newest period on arrival, swaps to an older one on its button, and closes on the second click', () => {
    mount()
    expect(detail('2026-08')).not.toBeNull()
    expect(detail('2026-07')).toBeNull()
    expect(toggle('2026-08').textContent).toBe('Hide')
    expect(toggle('2026-07').textContent).toBe('Consumption')
    // August's consumption reads the committed head short of its 1,500.
    expect(detail('2026-08')!.textContent).toContain('1,000 / 1,500 instance-hour')
    expect(detail('2026-08')!.textContent).toContain('500 instance-hour committed but not used')

    act(() => toggle('2026-07').click())
    expect(detail('2026-07')).not.toBeNull()
    expect(detail('2026-08')).toBeNull()
    expect(detail('2026-07')!.textContent).toContain('1,500 / 1,500 instance-hour')
    expect(detail('2026-07')!.textContent).toContain('100 % used, 3,500 instance-hour above it at list')
    expect(toggle('2026-07').getAttribute('aria-expanded')).toBe('true')
    expect(toggle('2026-08').getAttribute('aria-expanded')).toBe('false')

    act(() => toggle('2026-07').click())
    expect(detail('2026-07')).toBeNull()
    expect(detail('2026-08')).toBeNull()
    // The read-only principal sees the periods and none of the write controls.
    expect(host!.textContent).not.toContain('Add committed use')
    expect(host!.querySelector('a[href="/statements/st-aug"]')).not.toBeNull()
  })
})
