/**
 * SwitchoverDialog.test.tsx — Vitest unit tests for the EPIC-6 Slice
 * U-DR-1 (#1101) confirm dialog + the armed RPO/health preflight (#4552).
 *
 * The original suite uses the disableNetwork seam so no fetch is required.
 * The preflight suite drives the real (mocked) preview + switchover calls to
 * lock the arm-gate: Confirm is enabled ONLY when the preflight is promotable.
 */
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

const getSwitchoverPreview = vi.fn()
const requestSwitchover = vi.fn()

vi.mock('@/lib/continuum.api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/continuum.api')>()
  return {
    ...actual,
    getSwitchoverPreview: (...a: unknown[]) => getSwitchoverPreview(...a),
    requestSwitchover: (...a: unknown[]) => requestSwitchover(...a),
  }
})

import { SwitchoverDialog } from './SwitchoverDialog'
import { SWITCHOVER_STEPS } from '@/lib/continuum.api'

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('SwitchoverDialog', () => {
  it('renders the diff with from-region → to-region', () => {
    render(
      <SwitchoverDialog
        sovereignId="dep-1"
        continuumName="dr-wp"
        applicationName="wp-prod"
        fromRegion="hz-fsn-rtz-prod"
        toRegion="hz-hel-rtz-prod"
        onClose={() => {}}
        disableNetwork
      />,
    )
    const diff = screen.getByTestId('continuum-switchover-dialog-diff')
    expect(diff.textContent).toContain('hz-fsn-rtz-prod')
    expect(diff.textContent).toContain('hz-hel-rtz-prod')
  })

  it('renders all 7 steps from K-Cont-2 SWITCHOVER_STEPS', () => {
    render(
      <SwitchoverDialog
        sovereignId="dep-1"
        continuumName="dr-wp"
        applicationName="wp-prod"
        fromRegion="a"
        toRegion="b"
        onClose={() => {}}
        disableNetwork
      />,
    )
    for (const step of SWITCHOVER_STEPS) {
      expect(screen.getByTestId(`continuum-switchover-dialog-step-${step.id}`)).toBeTruthy()
    }
  })

  it('cancel calls onClose without triggering confirm', () => {
    const onClose = vi.fn()
    const onConfirmed = vi.fn()
    render(
      <SwitchoverDialog
        sovereignId="dep-1"
        continuumName="dr-wp"
        applicationName="wp-prod"
        fromRegion="a"
        toRegion="b"
        onClose={onClose}
        onConfirmed={onConfirmed}
        disableNetwork
      />,
    )
    fireEvent.click(screen.getByTestId('continuum-switchover-dialog-cancel'))
    expect(onClose).toHaveBeenCalled()
    expect(onConfirmed).not.toHaveBeenCalled()
  })

  it('confirm calls onConfirmed when network is disabled', () => {
    const onClose = vi.fn()
    const onConfirmed = vi.fn()
    render(
      <SwitchoverDialog
        sovereignId="dep-1"
        continuumName="dr-wp"
        applicationName="wp-prod"
        fromRegion="a"
        toRegion="b"
        onClose={onClose}
        onConfirmed={onConfirmed}
        disableNetwork
      />,
    )
    fireEvent.click(screen.getByTestId('continuum-switchover-dialog-confirm'))
    expect(onConfirmed).toHaveBeenCalled()
    expect(onClose).toHaveBeenCalled()
  })

  it('renders estimated duration + write-disruption block', () => {
    render(
      <SwitchoverDialog
        sovereignId="dep-1"
        continuumName="dr-wp"
        applicationName="wp-prod"
        fromRegion="a"
        toRegion="b"
        onClose={() => {}}
        disableNetwork
      />,
    )
    const est = screen.getByTestId('continuum-switchover-dialog-estimates')
    expect(est.textContent).toContain('60s')
    expect(est.textContent).toContain('5s')
  })
})

describe('SwitchoverDialog — RPO/health preflight arm-gate (#4552)', () => {
  beforeEach(() => {
    getSwitchoverPreview.mockReset()
    requestSwitchover.mockReset()
  })

  function renderNetworked(overrides: Record<string, unknown> = {}) {
    return render(
      <SwitchoverDialog
        sovereignId="dep-z"
        continuumName="dr-shared-pg"
        namespace="shared-data"
        applicationName="shared-pg"
        fromRegion="me-east-215-a"
        toRegion="me-east-215-b"
        onClose={vi.fn()}
        onConfirmed={vi.fn()}
        {...overrides}
      />,
    )
  }

  it('runs the preflight on open and arms Confirm only after a promotable result', async () => {
    getSwitchoverPreview.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      targetRegion: 'me-east-215-b',
      currentPrimary: 'me-east-215-a',
      currentLagSec: 0,
      estimatedDurationSec: 60,
      estimatedDuration: '60s',
      blockingChecks: [],
      promotable: true,
      message: 'preview only',
    })
    renderNetworked()

    // The preflight fires against the switchover-preview endpoint.
    await waitFor(() => {
      expect(getSwitchoverPreview).toHaveBeenCalledWith(
        'dep-z',
        'dr-shared-pg',
        { targetRegion: 'me-east-215-b' },
        { namespace: 'shared-data' },
      )
    })
    // Confirm is armed once the preflight clears.
    await waitFor(() => {
      const confirm = screen.getByTestId('continuum-switchover-dialog-confirm') as HTMLButtonElement
      expect(confirm.disabled).toBe(false)
    })
    expect(screen.getByTestId('continuum-switchover-dialog-preflight-status').textContent).toContain('ready')
    // Live lag reading is surfaced (not a hardcoded dash).
    expect(screen.getByTestId('continuum-switchover-dialog-preflight-lag').textContent).toContain('0.0 s')
  })

  it('keeps Confirm DISABLED when the preflight has blocking checks', async () => {
    getSwitchoverPreview.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      targetRegion: 'me-east-215-b',
      currentPrimary: 'me-east-215-a',
      currentLagSec: 320,
      estimatedDurationSec: 320,
      estimatedDuration: '320s',
      blockingChecks: ['WAL lag 320.0s exceeds 4× RTO (60s) — replica not promotable'],
      promotable: false,
      message: 'preview only',
    })
    renderNetworked()

    await waitFor(() => {
      expect(screen.getByTestId('continuum-switchover-dialog-preflight-checks')).toBeTruthy()
    })
    const confirm = screen.getByTestId('continuum-switchover-dialog-confirm') as HTMLButtonElement
    expect(confirm.disabled).toBe(true)
    expect(screen.getByTestId('continuum-switchover-dialog-preflight-check-0').textContent).toContain(
      'not promotable',
    )
    // Clicking the disabled Confirm does not fire the switchover mutation.
    fireEvent.click(confirm)
    expect(requestSwitchover).not.toHaveBeenCalled()
  })

  it('on a passing preflight + Confirm, fires the switchover and reports the applied outcome', async () => {
    getSwitchoverPreview.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      targetRegion: 'me-east-215-b',
      currentPrimary: 'me-east-215-a',
      currentLagSec: 0,
      estimatedDurationSec: 60,
      estimatedDuration: '60s',
      blockingChecks: [],
      promotable: true,
      message: 'preview only',
    })
    requestSwitchover.mockResolvedValue({
      name: 'dr-shared-pg',
      namespace: 'shared-data',
      targetRegion: 'me-east-215-b',
      fromRegion: 'me-east-215-a',
      toRegion: 'me-east-215-b',
      requestedAt: new Date().toISOString(),
      message: 'switchover completed',
      applied: true,
      completed: true,
    })
    const onConfirmed = vi.fn()
    const onClose = vi.fn()
    renderNetworked({ onConfirmed, onClose })

    await waitFor(() => {
      const confirm = screen.getByTestId('continuum-switchover-dialog-confirm') as HTMLButtonElement
      expect(confirm.disabled).toBe(false)
    })
    fireEvent.click(screen.getByTestId('continuum-switchover-dialog-confirm'))

    await waitFor(() => {
      expect(requestSwitchover).toHaveBeenCalledWith(
        'dep-z',
        'dr-shared-pg',
        { targetRegion: 'me-east-215-b', reason: undefined },
        { namespace: 'shared-data' },
      )
    })
    await waitFor(() => {
      expect(onConfirmed).toHaveBeenCalled()
    })
    expect(onClose).toHaveBeenCalled()
  })

  it('a 200-with-error switchover (no-live-dr-pair) keeps the dialog open and surfaces the reason', async () => {
    getSwitchoverPreview.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      targetRegion: 'me-east-215-b',
      currentPrimary: 'me-east-215-a',
      currentLagSec: 0,
      estimatedDurationSec: 60,
      estimatedDuration: '60s',
      blockingChecks: [],
      promotable: true,
      message: 'preview only',
    })
    requestSwitchover.mockResolvedValue({
      name: 'dr-shared-pg',
      namespace: 'shared-data',
      targetRegion: 'me-east-215-b',
      requestedAt: new Date().toISOString(),
      message: 'no live 2-region cnpg-pair backing shared-pg',
      applied: false,
      error: 'no-live-dr-pair',
    })
    const onConfirmed = vi.fn()
    const onClose = vi.fn()
    renderNetworked({ onConfirmed, onClose })

    await waitFor(() => {
      const confirm = screen.getByTestId('continuum-switchover-dialog-confirm') as HTMLButtonElement
      expect(confirm.disabled).toBe(false)
    })
    fireEvent.click(screen.getByTestId('continuum-switchover-dialog-confirm'))

    await waitFor(() => {
      expect(screen.getByTestId('continuum-switchover-dialog-error').textContent).toContain('no live 2-region')
    })
    expect(onConfirmed).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
  })
})
