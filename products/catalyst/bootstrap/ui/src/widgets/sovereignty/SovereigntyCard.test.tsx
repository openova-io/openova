/**
 * SovereigntyCard.test.tsx — unit coverage for every rendering branch
 * of the admin-console sovereignty surface (openova-io/openova#793).
 *
 * Strategy: the card accepts an `override?: UseCutoverEventsResult` prop
 * that bypasses the live hook. Every test materialises the exact
 * snapshot the SSE reducer would produce in a given state and asserts
 * the visible output. The hook itself has its own tests in
 * useCutoverEvents.test.tsx; here we focus on the COMPONENT shape.
 *
 * The matrix:
 *   1. Tethered, no progress     → badge="Tethered" + CTA visible
 *   2. CTA opens explanation modal
 *   3. Modal "Cancel" closes, no fetch
 *   4. Modal "Start cutover" calls startCutover()
 *   5. startCutover error surfaces inside the modal
 *   6. Tethered, in-flight        → no CTA, progress card mounted
 *   7. Sovereign, terminal       → green badge + summary stats
 *   8. Failed step renders red message in progress card
 *   9. Stream-level error renders amber banner when no progress card
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import {
  render,
  screen,
  cleanup,
  fireEvent,
  act,
} from '@testing-library/react'
import { SovereigntyCard } from './SovereigntyCard'
import type { UseCutoverEventsResult } from './useCutoverEvents'
import {
  buildInitialCutoverStatus,
  CUTOVER_STEPS,
  parseCutoverStatus,
  type CutoverStatus,
} from '@/shared/types/cutover'

afterEach(() => cleanup())

function makeResult(overrides: Partial<UseCutoverEventsResult>): UseCutoverEventsResult {
  return {
    status: buildInitialCutoverStatus(),
    streamStatus: 'streaming',
    streamError: null,
    startCutover: vi.fn().mockResolvedValue(undefined),
    retry: vi.fn(),
    starting: false,
    startError: null,
    ...overrides,
  }
}

describe('SovereigntyCard — tethered, no progress', () => {
  it('renders the Tethered badge and the Achieve True Sovereignty CTA', () => {
    render(<SovereigntyCard override={makeResult({})} />)
    expect(screen.getByTestId('sovereignty-card')).toBeTruthy()
    expect(screen.getByTestId('sovereignty-badge').textContent).toMatch(/tethered/i)
    expect(screen.getByTestId('cutover-start-button').textContent).toMatch(
      /achieve true sovereignty/i,
    )
    // No progress card before any step has moved.
    expect(screen.queryByTestId('cutover-progress-card')).toBeNull()
    // No summary stats while still tethered.
    expect(screen.queryByTestId('sovereignty-stats')).toBeNull()
  })

  it('sets data-cutover-state="tethered" on the wrapper', () => {
    render(<SovereigntyCard override={makeResult({})} />)
    const wrap = screen.getByTestId('sovereignty-card')
    expect(wrap.getAttribute('data-cutover-state')).toBe('tethered')
  })
})

describe('SovereigntyCard — CTA opens explanation modal', () => {
  it('clicking the button reveals the modal with the per-step explainer', () => {
    render(<SovereigntyCard override={makeResult({})} />)
    fireEvent.click(screen.getByTestId('cutover-start-button'))
    const modal = screen.getByTestId('cutover-confirm-modal')
    expect(modal).toBeTruthy()
    // One numbered list item per canonical cutover step.
    const list = modal.querySelector('ol')
    expect(list).toBeTruthy()
    expect(list!.children.length).toBe(CUTOVER_STEPS.length)
    // Confirm + cancel buttons are both wired.
    expect(screen.getByTestId('cutover-confirm-button')).toBeTruthy()
    expect(screen.getByTestId('cutover-confirm-cancel')).toBeTruthy()
  })
})

describe('SovereigntyCard — Confirm fires startCutover', () => {
  it('clicking Start cutover invokes the start handler', async () => {
    const start = vi.fn().mockResolvedValue(undefined)
    render(<SovereigntyCard override={makeResult({ startCutover: start })} />)
    fireEvent.click(screen.getByTestId('cutover-start-button'))
    await act(async () => {
      fireEvent.click(screen.getByTestId('cutover-confirm-button'))
    })
    expect(start).toHaveBeenCalledTimes(1)
  })
})

describe('SovereigntyCard — Cancel does not fire startCutover', () => {
  it('clicking Cancel closes the modal without calling start', () => {
    const start = vi.fn().mockResolvedValue(undefined)
    render(<SovereigntyCard override={makeResult({ startCutover: start })} />)
    fireEvent.click(screen.getByTestId('cutover-start-button'))
    fireEvent.click(screen.getByTestId('cutover-confirm-cancel'))
    expect(start).not.toHaveBeenCalled()
  })
})

describe('SovereigntyCard — start error surfaces in modal', () => {
  it('renders the error banner when startError is non-null', () => {
    render(
      <SovereigntyCard
        override={makeResult({ startError: 'cutover start returned HTTP 503' })}
      />,
    )
    fireEvent.click(screen.getByTestId('cutover-start-button'))
    const err = screen.getByTestId('cutover-confirm-error')
    expect(err.textContent).toMatch(/HTTP 503/)
  })
})

describe('SovereigntyCard — in-flight rendering', () => {
  it('hides the CTA and mounts the progress card when a step is running', () => {
    const status: CutoverStatus = parseCutoverStatus({
      state: 'tethered',
      steps: [
        {
          step: 'gitea-mirror',
          status: 'running',
          startedAt: '2026-05-04T10:00:00Z',
        },
        // Other steps remain pending — buildInitial provides them.
      ],
    })
    // Re-seed all canonical steps so the card renders all 8 rows.
    const merged: CutoverStatus = {
      ...status,
      steps: CUTOVER_STEPS.map((s) =>
        status.steps.find((x) => x.step === s.id) ?? {
          step: s.id,
          status: 'pending',
        },
      ),
    }
    render(<SovereigntyCard override={makeResult({ status: merged })} />)
    expect(screen.queryByTestId('cutover-start-button')).toBeNull()
    expect(screen.getByTestId('cutover-progress-card')).toBeTruthy()
    // All 8 step rows present, even pending ones.
    for (const s of CUTOVER_STEPS) {
      expect(screen.getByTestId(`cutover-step-${s.id}`)).toBeTruthy()
    }
  })
})

describe('SovereigntyCard — sovereign terminal', () => {
  it('shows the Sovereign badge + summary stats + achieved frame', () => {
    const status: CutoverStatus = parseCutoverStatus({
      state: 'sovereign',
      steps: CUTOVER_STEPS.map((s) => ({
        step: s.id,
        status: 'done',
        finishedAt: '2026-05-04T10:01:00Z',
      })),
      mirroredCommitSHA: 'deadbeef00112233',
      harborProjectCount: 7,
      egressTestPassed: true,
    })
    render(
      <SovereigntyCard
        override={makeResult({ status, streamStatus: 'completed' })}
      />,
    )
    expect(screen.getByTestId('sovereignty-badge').textContent).toMatch(
      /sovereign/i,
    )
    // Card-level stats.
    expect(screen.getByTestId('sovereignty-stats')).toBeTruthy()
    expect(screen.getByTestId('sovereignty-stat-sha').textContent).toMatch(
      /deadbeef0011/,
    )
    expect(screen.getByTestId('sovereignty-stat-harbor').textContent).toMatch(/7/)
    expect(screen.getByTestId('sovereignty-stat-egress').textContent).toMatch(
      /passed/,
    )
    // Progress-card terminal frame shows the same data.
    expect(screen.getByTestId('cutover-achieved-summary')).toBeTruthy()
    // No more CTA in terminal state.
    expect(screen.queryByTestId('cutover-start-button')).toBeNull()
    // Wrapper carries the data-cutover-state attribute for E2E.
    expect(
      screen.getByTestId('sovereignty-card').getAttribute('data-cutover-state'),
    ).toBe('sovereign')
  })
})

describe('SovereigntyCard — failed step', () => {
  it('renders the failure message inline on the failed step row', () => {
    const status: CutoverStatus = parseCutoverStatus({
      state: 'tethered',
      steps: [
        {
          step: 'gitea-mirror',
          status: 'done',
          finishedAt: '2026-05-04T10:01:00Z',
        },
        {
          step: 'harbor-projects',
          status: 'failed',
          finishedAt: '2026-05-04T10:02:00Z',
          message: 'harbor admin password rejected',
        },
      ],
    })
    const merged: CutoverStatus = {
      ...status,
      steps: CUTOVER_STEPS.map((s) =>
        status.steps.find((x) => x.step === s.id) ?? {
          step: s.id,
          status: 'pending',
        },
      ),
    }
    render(<SovereigntyCard override={makeResult({ status: merged })} />)
    const errEl = screen.getByTestId('cutover-step-harbor-projects-error')
    expect(errEl.textContent).toMatch(/password rejected/)
  })
})

describe('SovereigntyCard — stream-level error', () => {
  it('renders an amber banner when streamError is present and no progress is in flight', () => {
    render(
      <SovereigntyCard
        override={makeResult({
          streamError:
            'cutover API not yet available on this Sovereign — try again after the bp-self-sovereign-cutover chart is installed',
          streamStatus: 'unreachable',
        })}
      />,
    )
    const err = screen.getByTestId('sovereignty-stream-error')
    expect(err.textContent).toMatch(/not yet available/)
  })
})
