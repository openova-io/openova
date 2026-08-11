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

  it('directory view (no org prop) renders EVERY org — the customer Org row, not just the parent (#4739 row 23)', () => {
    const multi: SovereignConsumption = {
      totalCostUnits: 1425.5,
      pending: false,
      orgs: [
        { org: 'sovereign', isParent: true, isPlatform: false, costUnits: 0, cpuMilli: 0, memoryGiB: 0, storageGiB: 0, apps: [] },
        { org: 'uatwp4758', isParent: false, isPlatform: false, costUnits: 525.5, cpuMilli: 520, memoryGiB: 1.06, storageGiB: 5,
          apps: [{ application: 'vcluster', namespace: 'uatwp4758', costUnits: 525.5, cpuMilli: 520, memoryGiB: 1.06, storageGiB: 5, percent: 100 }] },
        { org: '__platform', isParent: false, isPlatform: true, costUnits: 900, cpuMilli: 750, memoryGiB: 2, storageGiB: 10, apps: [] },
      ],
    }
    renderPanel(multi)
    // The customer Org slice MUST render (the bug: it was dropped, only the parent showed).
    expect(screen.getByTestId('showback-org-slice-uatwp4758')).toBeTruthy()
    expect(screen.getByTestId('showback-app-vcluster')).toBeTruthy()
    // All three orgs present + distinct (parent, customer, platform).
    expect(screen.getByTestId('showback-org-slice-sovereign')).toBeTruthy()
    expect(screen.getByTestId('showback-org-slice-__platform')).toBeTruthy()
    // The customer Org's 525.5 units surface (not collapsed to the parent's 0).
    expect(screen.getAllByTestId('showback-total').some((n) => n.textContent === '525.5')).toBe(true)
  })

  it('detail view (org prop) still renders only that one org', () => {
    const multi: SovereignConsumption = {
      totalCostUnits: 525.5, pending: false,
      orgs: [
        { org: 'sovereign', isParent: true, isPlatform: false, costUnits: 0, cpuMilli: 0, memoryGiB: 0, storageGiB: 0, apps: [] },
        { org: 'uatwp4758', isParent: false, isPlatform: false, costUnits: 525.5, cpuMilli: 520, memoryGiB: 1.06, storageGiB: 5,
          apps: [{ application: 'vcluster', namespace: 'uatwp4758', costUnits: 525.5, cpuMilli: 520, memoryGiB: 1.06, storageGiB: 5, percent: 100 }] },
      ],
    }
    renderPanel(multi, 'uatwp4758')
    expect(screen.getByTestId('showback-org-slice-uatwp4758')).toBeTruthy()
    expect(screen.queryByTestId('showback-org-slice-sovereign')).toBeNull()
  })

  it('never crashes on an empty feed', () => {
    renderPanel({ totalCostUnits: 0, pending: true, orgs: [] })
    expect(screen.getByTestId('showback-empty')).toBeTruthy()
  })

  // #6114 / UAT row 25(c) — hw293 billed `g7doora` 4272.25 units as a
  // customer Organization while `kubectl get organizations` did not list
  // it. The API now routes that consumption to the synthetic unowned
  // rollup; the panel must say what it is rather than calling it an
  // Organization or printing the raw `__unowned__` sentinel.
  it('names the unowned rollup instead of claiming it is an Organization', () => {
    const orphaned: SovereignConsumption = {
      totalCostUnits: 2168,
      pending: false,
      unownedOrgs: ['g7doora'],
      orgs: [
        { org: 'sovereign', isParent: true, isPlatform: false, costUnits: 0, cpuMilli: 0, memoryGiB: 0, storageGiB: 0, apps: [] },
        { org: 'hw293vch', isParent: false, isPlatform: false, costUnits: 404, cpuMilli: 400, memoryGiB: 1, storageGiB: 0,
          apps: [{ application: 'bp-newapi', namespace: 'hw293vch', costUnits: 404, cpuMilli: 400, memoryGiB: 1, storageGiB: 0, percent: 100 }] },
        { org: '__unowned__', isParent: false, isPlatform: false, isUnowned: true, costUnits: 1764, cpuMilli: 1750, memoryGiB: 3.5, storageGiB: 0,
          apps: [{ application: 'bp-keycloak', namespace: 'g7doora', costUnits: 1008, cpuMilli: 1000, memoryGiB: 2, storageGiB: 0, percent: 57.14 }] },
      ],
    }
    renderPanel(orphaned)

    // The row is labelled honestly — not "(Organization)", not the sentinel.
    const labels = screen.getAllByTestId('showback-org').map((n) => n.textContent)
    expect(labels).toContain('Unowned namespaces')
    expect(labels).not.toContain('__unowned__')

    // The orphaned slug is named explicitly, which is the whole point of
    // not billing it silently.
    expect(screen.getByTestId('showback-unowned-slugs').textContent).toBe('g7doora')

    // CONTROL: the genuine Organization alongside it still renders in full.
    expect(screen.getByTestId('showback-org-slice-hw293vch')).toBeTruthy()
    expect(screen.getAllByTestId('showback-total').some((n) => n.textContent === '404')).toBe(true)
  })

  it('shows no unowned warning on a healthy estate', () => {
    renderPanel(FEED)
    expect(screen.queryByTestId('showback-unowned-warning')).toBeNull()
  })
})
