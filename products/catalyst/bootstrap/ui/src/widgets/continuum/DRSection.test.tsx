/**
 * DRSection.test.tsx — Vitest unit tests for the EPIC-6 Slice U-DR-1
 * (#1101) DR section orchestrator. Uses the initialContinuum +
 * disableNetwork test seams to render without a real fetch.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { DRSection } from './DRSection'
import type { ContinuumGetResponse } from '@/lib/continuum.api'

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

afterEach(() => cleanup())

const sampleCR: ContinuumGetResponse = {
  name: 'dr-wp',
  namespace: 'acme',
  uid: 'uid-1',
  spec: {
    applicationRef: 'wp-prod',
    primaryRegion: 'hz-fsn-rtz-prod',
    hotStandbyRegions: ['hz-hel-rtz-prod'],
    leaseClient: { kind: 'cloudflare-kv' },
  },
  status: {
    phase: 'Healthy',
    primaryRegion: 'hz-fsn-rtz-prod',
    leaseHolder: 'hz-fsn-rtz-prod',
    replicationLagSeconds: 12,
  },
}

describe('DRSection', () => {
  it('renders the DR header + status panel from initialContinuum', () => {
    render(
      withProviders(
        <DRSection
          sovereignId="dep-1"
          continuumName="dr-wp"
          applicationName="wp-prod"
          callerTier="owner"
          initialContinuum={sampleCR}
          disableNetwork
        />,
      ),
    )
    expect(screen.getByTestId('continuum-dr-section')).toBeTruthy()
    expect(screen.getByTestId('continuum-status-panel')).toBeTruthy()
  })

  it('shows the Switchover button for owner tier with a hot-standby target', () => {
    render(
      withProviders(
        <DRSection
          sovereignId="dep-1"
          continuumName="dr-wp"
          applicationName="wp-prod"
          callerTier="owner"
          initialContinuum={sampleCR}
          disableNetwork
        />,
      ),
    )
    expect(screen.getByTestId('continuum-dr-switchover-btn')).toBeTruthy()
  })

  it('hides the Switchover button for viewer tier', () => {
    render(
      withProviders(
        <DRSection
          sovereignId="dep-1"
          continuumName="dr-wp"
          applicationName="wp-prod"
          callerTier="viewer"
          initialContinuum={sampleCR}
          disableNetwork
        />,
      ),
    )
    expect(screen.queryByTestId('continuum-dr-switchover-btn')).toBeNull()
    expect(screen.getByTestId('continuum-dr-switchover-disabled')).toBeTruthy()
  })

  it('clicking Switchover opens the SwitchoverDialog', () => {
    render(
      withProviders(
        <DRSection
          sovereignId="dep-1"
          continuumName="dr-wp"
          applicationName="wp-prod"
          callerTier="owner"
          initialContinuum={sampleCR}
          disableNetwork
        />,
      ),
    )
    fireEvent.click(screen.getByTestId('continuum-dr-switchover-btn'))
    expect(screen.getByTestId('continuum-switchover-dialog')).toBeTruthy()
  })

  it('renders the Failback panel when the last switchover succeeded', () => {
    const cr: ContinuumGetResponse = {
      ...sampleCR,
      status: {
        ...sampleCR.status,
        lastSwitchover: { result: 'Success', from: 'a', to: 'b', at: '2026-05-08T14:23:11Z' },
      },
    }
    render(
      withProviders(
        <DRSection
          sovereignId="dep-1"
          continuumName="dr-wp"
          applicationName="wp-prod"
          callerTier="owner"
          initialContinuum={cr}
          disableNetwork
        />,
      ),
    )
    expect(screen.getByTestId('continuum-failback-panel')).toBeTruthy()
  })

  it('renders the lua-record view collapsed', () => {
    render(
      withProviders(
        <DRSection
          sovereignId="dep-1"
          continuumName="dr-wp"
          applicationName="wp-prod"
          callerTier="owner"
          initialContinuum={sampleCR}
          disableNetwork
        />,
      ),
    )
    expect(screen.getByTestId('continuum-lua-view')).toBeTruthy()
  })
})

/**
 * #3375 (DR-UI honesty) — the Switchover control must be ARMED only when a
 * REAL live DR pair backs the app, and DISABLED-with-an-honest-reason when
 * none exists. This locks the fix for the hw158 defect: an armed Switchover
 * against a non-existent dr-grafana that 404s on click.
 */
describe('DRSection — Switchover armed only with a real live DR pair (#3375)', () => {
  it('ARMS the Switchover button for an owner when drPairLive=true', () => {
    render(
      withProviders(
        <DRSection
          sovereignId="dep-1"
          continuumName="dr-grafana"
          applicationName="grafana"
          callerTier="owner"
          declaredClass="active-hot-standby"
          drPairLive
          initialContinuum={sampleCR}
          disableNetwork
        />,
      ),
    )
    expect(screen.getByTestId('continuum-dr-switchover-btn')).toBeTruthy()
    expect(screen.queryByTestId('continuum-dr-switchover-no-pair')).toBeNull()
  })

  it('DISABLES the Switchover with an honest reason when drPairLive=false (owner, no phantom arm)', () => {
    render(
      withProviders(
        <DRSection
          sovereignId="dep-1"
          continuumName="dr-grafana"
          applicationName="grafana"
          callerTier="owner"
          declaredClass="active-hot-standby"
          drPairLive={false}
          initialContinuum={sampleCR}
          disableNetwork
        />,
      ),
    )
    // No armed button — the exact hw158 defect (armed against a phantom CR).
    expect(screen.queryByTestId('continuum-dr-switchover-btn')).toBeNull()
    // Instead, the honest disabled state.
    const disabled = screen.getByTestId('continuum-dr-switchover-no-pair')
    expect(disabled).toBeTruthy()
    expect(disabled.textContent ?? '').toMatch(/no live dr pair/i)
  })

  it('does NOT arm Switchover when no live CR exists and no drPairLive signal (old crMissing arming is gone)', () => {
    // disableNetwork + no initialContinuum → the panel has no live record.
    // The OLD behaviour armed the button on this `crMissing` case; the fix
    // makes it honestly disabled.
    render(
      withProviders(
        <DRSection
          sovereignId="dep-1"
          continuumName="dr-grafana"
          applicationName="grafana"
          callerTier="owner"
          declaredClass="active-hot-standby"
          disableNetwork
        />,
      ),
    )
    expect(screen.queryByTestId('continuum-dr-switchover-btn')).toBeNull()
    expect(screen.getByTestId('continuum-dr-switchover-no-pair')).toBeTruthy()
  })

  it('still hides the control for a non-owner even when a live pair exists', () => {
    render(
      withProviders(
        <DRSection
          sovereignId="dep-1"
          continuumName="dr-grafana"
          applicationName="grafana"
          callerTier="viewer"
          declaredClass="active-hot-standby"
          drPairLive
          initialContinuum={sampleCR}
          disableNetwork
        />,
      ),
    )
    expect(screen.queryByTestId('continuum-dr-switchover-btn')).toBeNull()
    expect(screen.getByTestId('continuum-dr-switchover-disabled')).toBeTruthy()
  })
})
