/**
 * ShowbackPanel.test.tsx — the B3 parent self-showback panel (issue
 * #3378 DoD 3 + §5). The parent's per-app cost attribution renders, the
 * pending flag surfaces, and the empty estate never crashes.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ShowbackPanel } from './ShowbackPanel'
import type { SovereignConsumption } from '@/lib/organizations.api'

function renderPanel(feed: SovereignConsumption, org?: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <ShowbackPanel initialOverride={feed} org={org} />
    </QueryClientProvider>,
  )
}

const FEED: SovereignConsumption = {
  totalCostUnits: 900,
  pending: false,
  orgs: [
    {
      org: 'hw130.omantel.biz',
      isParent: true,
      costUnits: 900,
      cpuMilli: 750,
      memoryGiB: 2,
      storageGiB: 10,
      apps: [
        { application: 'catalyst-api', namespace: 'catalyst', costUnits: 504, cpuMilli: 500, memoryGiB: 1, storageGiB: 0, percent: 56 },
        { application: 'grafana', namespace: 'monitoring', costUnits: 396, cpuMilli: 250, memoryGiB: 1, storageGiB: 10, percent: 44 },
      ],
    },
  ],
}

afterEach(() => cleanup())

describe('ShowbackPanel — parent self-showback (DoD 3)', () => {
  it('renders the parent org with its per-app attribution', () => {
    renderPanel(FEED)
    expect(screen.getByTestId('showback-panel')).toBeTruthy()
    expect(screen.getByTestId('showback-org').textContent).toBe('hw130.omantel.biz')
    expect(screen.getByTestId('showback-app-catalyst-api')).toBeTruthy()
    expect(screen.getByTestId('showback-app-grafana')).toBeTruthy()
    expect(screen.getByTestId('showback-app-pct-catalyst-api').textContent).toBe('56%')
  })

  it('flags the pending state while metering warms up', () => {
    renderPanel({ totalCostUnits: 0, pending: true, orgs: [{ org: 'sovereign', isParent: true, costUnits: 0, cpuMilli: 0, memoryGiB: 0, storageGiB: 0, apps: [] }] })
    expect(screen.getByTestId('showback-pending')).toBeTruthy()
    expect(screen.getByTestId('showback-no-apps')).toBeTruthy()
  })

  it('never crashes on an empty feed', () => {
    renderPanel({ totalCostUnits: 0, pending: true, orgs: [] })
    expect(screen.getByTestId('showback-empty')).toBeTruthy()
  })
})
