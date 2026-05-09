/**
 * SwitchoverHistory.test.tsx — Vitest unit tests for the EPIC-6 Slice
 * U-DR-1 (#1101) audit-history table.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

import { SwitchoverHistory } from './SwitchoverHistory'
import type { ContinuumAuditEvent } from '@/lib/continuum.api'

afterEach(() => cleanup())

const sampleEvents: ContinuumAuditEvent[] = [
  {
    auditType: 'continuum-switchover',
    ts: '2026-05-08T14:23:11Z',
    actor: 'owner@acme.io',
    sovereignId: 'dep-1',
    targetApp: 'wp-prod',
    detail: 'switchover requested: hz-fsn-rtz-prod → hz-hel-rtz-prod (reason: drill)',
    result: 'ok',
  },
  {
    auditType: 'continuum-cnpg-promotable',
    ts: '2026-05-08T14:23:09Z',
    sovereignId: 'dep-1',
    targetApp: 'wp-prod',
    detail: 'standby promotable; lag=4s',
  },
  {
    auditType: 'continuum-lease-acquired',
    ts: '2026-05-08T14:23:13Z',
    sovereignId: 'dep-1',
    targetApp: 'wp-prod',
    detail: 'acquired lease for region hz-hel-rtz-prod',
  },
  {
    auditType: 'continuum-error',
    ts: '2026-05-08T14:23:14Z',
    sovereignId: 'dep-1',
    targetApp: 'wp-prod',
    detail: 'transient error',
    result: 'error',
  },
  // Different app — should be filtered out when applicationName provided.
  {
    auditType: 'continuum-switchover',
    ts: '2026-05-09T01:00:00Z',
    actor: 'someone-else@acme.io',
    sovereignId: 'dep-1',
    targetApp: 'other-app',
    detail: 'unrelated',
    result: 'ok',
  },
]

describe('SwitchoverHistory', () => {
  it('renders a row per switchover event matching the application', () => {
    render(<SwitchoverHistory events={sampleEvents} applicationName="wp-prod" />)
    expect(screen.getByTestId('continuum-history-row-0')).toBeTruthy()
    // Only one matching switchover row for wp-prod (the second is "other-app").
    expect(screen.queryByTestId('continuum-history-row-1')).toBeNull()
  })

  it('renders empty state when no events match', () => {
    render(<SwitchoverHistory events={[]} applicationName="wp-prod" />)
    expect(screen.getByTestId('continuum-history-empty')).toBeTruthy()
  })

  it('clicking a row opens the detail modal with bundled step events', () => {
    render(<SwitchoverHistory events={sampleEvents} applicationName="wp-prod" />)
    fireEvent.click(screen.getByTestId('continuum-history-row-0'))
    expect(screen.getByTestId('continuum-history-modal')).toBeTruthy()
    // The modal should bundle nearby step events (cnpg-promotable, lease-acquired, error).
    expect(screen.getByTestId('continuum-history-modal-step-0')).toBeTruthy()
  })

  it('parses from→to from detail string into table columns', () => {
    render(<SwitchoverHistory events={sampleEvents} applicationName="wp-prod" />)
    const row = screen.getByTestId('continuum-history-row-0')
    expect(row.textContent).toContain('hz-fsn-rtz-prod')
    expect(row.textContent).toContain('hz-hel-rtz-prod')
  })
})
