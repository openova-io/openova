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
