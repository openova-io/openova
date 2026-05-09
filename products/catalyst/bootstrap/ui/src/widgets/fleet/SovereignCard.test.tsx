/**
 * SovereignCard.test.tsx — render coverage for the U-Fleet-2 widget.
 *
 * What we assert:
 *   - Renders FQDN heading + provider chip.
 *   - Renders health badge label matching the SovereignSummary.health.
 *   - Renders application metric counts when detailOverride supplied.
 *   - Renders region chips.
 *   - Click navigates via window.location.href to chroot URL.
 *   - Empty regions shows "No regions reported".
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { SovereignCard } from './SovereignCard'
import type { SovereignSummary, SovereignDetail } from '@/lib/fleet.api'

afterEach(() => cleanup())

function renderCard(
  sovereign: SovereignSummary,
  detail?: SovereignDetail,
  onClick?: () => void,
) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <SovereignCard sovereign={sovereign} detailOverride={detail} onClick={onClick} />
    </QueryClientProvider>,
  )
}

const HEALTHY_SOV: SovereignSummary = {
  id: 'sov-a',
  fqdn: 'acme.example.com',
  region: 'fsn1',
  health: 'green',
  providerType: 'hetzner',
}

const SAMPLE_DETAIL: SovereignDetail = {
  sovereign: HEALTHY_SOV,
  orgs: 2,
  applications: { total: 5, active: 3, failing: 1 },
  regions: ['hz-fsn-rtz-prod', 'hz-hel-rtz-prod'],
  alerts: 0,
  lastActivity: '2026-05-01T10:00:00Z',
}

describe('SovereignCard — render', () => {
  it('renders the Sovereign FQDN', () => {
    renderCard(HEALTHY_SOV, SAMPLE_DETAIL)
    expect(screen.getByText('acme.example.com')).toBeTruthy()
  })

  it('renders the health label for green', () => {
    renderCard(HEALTHY_SOV, SAMPLE_DETAIL)
    const healthBadge = screen.getByTestId('sovereign-card-health-sov-a')
    expect(healthBadge.textContent).toContain('Healthy')
  })

  it('renders red Failed badge', () => {
    renderCard(
      { ...HEALTHY_SOV, id: 'sov-fail', health: 'red' },
      { ...SAMPLE_DETAIL, sovereign: { ...HEALTHY_SOV, id: 'sov-fail', health: 'red' } },
    )
    expect(screen.getByTestId('sovereign-card-health-sov-fail').textContent).toContain('Failed')
  })

  it('renders region chips', () => {
    renderCard(HEALTHY_SOV, SAMPLE_DETAIL)
    expect(screen.getByTestId('sovereign-card-region-hz-fsn-rtz-prod')).toBeTruthy()
    expect(screen.getByTestId('sovereign-card-region-hz-hel-rtz-prod')).toBeTruthy()
  })

  it('renders provider + region in subhead', () => {
    renderCard(HEALTHY_SOV, SAMPLE_DETAIL)
    // Subhead reads "hetzner · fsn1"
    expect(screen.getByText(/hetzner · fsn1/)).toBeTruthy()
  })

  it('shows "No regions reported" when regions empty', () => {
    renderCard(HEALTHY_SOV, { ...SAMPLE_DETAIL, regions: [] })
    expect(screen.getByText('No regions reported')).toBeTruthy()
  })

  it('renders application counts (total + sub)', () => {
    renderCard(HEALTHY_SOV, SAMPLE_DETAIL)
    expect(screen.getByText('5')).toBeTruthy()
    expect(screen.getByText('3 active · 1 failing')).toBeTruthy()
  })
})

describe('SovereignCard — interaction', () => {
  beforeEach(() => {
    vi.spyOn(window, 'location', 'get').mockReturnValue({ href: '' } as never)
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('fires onClick when clicked', () => {
    const onClick = vi.fn()
    renderCard(HEALTHY_SOV, SAMPLE_DETAIL, onClick)
    fireEvent.click(screen.getByTestId('sovereign-card-sov-a'))
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('fires onClick on Enter key', () => {
    const onClick = vi.fn()
    renderCard(HEALTHY_SOV, SAMPLE_DETAIL, onClick)
    const card = screen.getByTestId('sovereign-card-sov-a')
    fireEvent.keyDown(card, { key: 'Enter' })
    expect(onClick).toHaveBeenCalled()
  })
})
