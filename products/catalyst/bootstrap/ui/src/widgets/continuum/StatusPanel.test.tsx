/**
 * StatusPanel.test.tsx — Vitest unit tests for the EPIC-6 Slice U-DR-1
 * (#1101) live-status panel.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'

import { StatusPanel } from './StatusPanel'

afterEach(() => cleanup())

describe('StatusPanel — phase rendering', () => {
  it('shows the Healthy badge', () => {
    render(<StatusPanel status={{ phase: 'Healthy' }} primaryRegion="hz-fsn-rtz-prod" />)
    expect(screen.getByTestId('continuum-status-phase-Healthy')).toBeTruthy()
  })

  it('shows the SwitchingOver badge + step', () => {
    render(
      <StatusPanel
        status={{ phase: 'SwitchingOver', switchoverInProgress: true, switchoverStep: 'step 3/7 — drain-http' }}
        primaryRegion="hz-fsn-rtz-prod"
      />,
    )
    expect(screen.getByTestId('continuum-status-switching-over')).toBeTruthy()
    const step = screen.getByTestId('continuum-status-switching-over-step')
    expect(step.textContent).toContain('3/7')
    expect(step.textContent).toContain('drain-http')
  })
})

describe('StatusPanel — lag color thresholds', () => {
  it('green when lag < 30s', () => {
    render(<StatusPanel status={{ replicationLagSeconds: 12 }} primaryRegion="a" />)
    expect(screen.getByTestId('continuum-status-lag-bucket-green')).toBeTruthy()
  })

  it('yellow when lag in 30..60', () => {
    render(<StatusPanel status={{ replicationLagSeconds: 45 }} primaryRegion="a" />)
    expect(screen.getByTestId('continuum-status-lag-bucket-yellow')).toBeTruthy()
  })

  it('red when lag > 60', () => {
    render(<StatusPanel status={{ replicationLagSeconds: 75 }} primaryRegion="a" />)
    expect(screen.getByTestId('continuum-status-lag-bucket-red')).toBeTruthy()
  })

  it('unknown when lag missing', () => {
    render(<StatusPanel status={{}} primaryRegion="a" />)
    expect(screen.getByTestId('continuum-status-lag-bucket-unknown')).toBeTruthy()
  })
})

describe('StatusPanel — last switchover side panel', () => {
  it('renders when status.lastSwitchover is present', () => {
    render(
      <StatusPanel
        status={{
          phase: 'FailedOver',
          lastSwitchover: { at: '2026-05-08T14:23:11Z', from: 'hz-fsn-rtz-prod', to: 'hz-hel-rtz-prod', result: 'Success' },
        }}
        primaryRegion="hz-hel-rtz-prod"
      />,
    )
    const panel = screen.getByTestId('continuum-status-last-switchover')
    expect(panel.textContent).toContain('hz-hel-rtz-prod')
    expect(screen.getByTestId('continuum-status-last-switchover-result').textContent).toBe('Success')
  })

  it('omits the panel when lastSwitchover is empty', () => {
    render(<StatusPanel status={{ phase: 'Healthy' }} primaryRegion="a" />)
    expect(screen.queryByTestId('continuum-status-last-switchover')).toBeNull()
  })
})

describe('StatusPanel — hot-standby badges', () => {
  it('renders one badge per hot-standby region', () => {
    render(
      <StatusPanel
        status={{ phase: 'Healthy' }}
        primaryRegion="hz-fsn-rtz-prod"
        hotStandbyRegions={['hz-hel-rtz-prod', 'hz-nbg-rtz-prod']}
      />,
    )
    expect(screen.getByTestId('continuum-status-hotstandby-hz-hel-rtz-prod')).toBeTruthy()
    expect(screen.getByTestId('continuum-status-hotstandby-hz-nbg-rtz-prod')).toBeTruthy()
  })
})
