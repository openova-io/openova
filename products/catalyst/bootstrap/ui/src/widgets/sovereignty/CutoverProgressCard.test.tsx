/**
 * CutoverProgressCard.test.tsx — locks in the per-step rendering
 * contract for the 8-step cutover progress card.
 *
 * What we assert:
 *   1. All 8 canonical step rows render at all times — the card never
 *      disappears partway through the chain.
 *   2. Pending / running / done / failed each set the `data-step-status`
 *      attribute to the right value (Playwright + e2e read this).
 *   3. The percentage badge counts only `done` steps.
 *   4. The terminal "Sovereignty achieved" summary materialises ONLY
 *      when `state === 'sovereign'`.
 *   5. A failed step's message renders inline.
 *   6. A stream-level error renders above the rows.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { CutoverProgressCard } from './CutoverProgressCard'
import { SovereigntyCard } from './SovereigntyCard'
import {
  applyCutoverStepUpdate,
  buildInitialCutoverStatus,
  CUTOVER_STEPS,
  parseCutoverStatus,
  parseCutoverStatusConfigMap,
  type CutoverStatus,
} from '@/shared/types/cutover'

afterEach(() => cleanup())

describe('CutoverProgressCard — first paint (all pending)', () => {
  it('renders all 8 canonical step rows with status=pending', () => {
    const seed: CutoverStatus = buildInitialCutoverStatus()
    render(<CutoverProgressCard status={seed} />)
    for (const s of CUTOVER_STEPS) {
      const row = screen.getByTestId(`cutover-step-${s.id}`)
      expect(row.getAttribute('data-step-status')).toBe('pending')
    }
    expect(screen.getByTestId('cutover-progress-pct').textContent).toMatch(/0%/)
  })
})

describe('CutoverProgressCard — mid-flight progress', () => {
  it('counts done steps for the percentage', () => {
    const status: CutoverStatus = parseCutoverStatus({
      state: 'tethered',
      steps: [
        { step: 'gitea-mirror', status: 'done' },
        { step: 'harbor-projects', status: 'done' },
        { step: 'harbor-prewarm', status: 'running' },
      ],
    })
    // Padding pending so the card renders all canonical rows.
    const merged: CutoverStatus = {
      ...status,
      steps: CUTOVER_STEPS.map((s) =>
        status.steps.find((x) => x.step === s.id) ?? {
          step: s.id,
          status: 'pending',
        },
      ),
    }
    render(<CutoverProgressCard status={merged} />)
    // 2 done of CUTOVER_STEPS.length, rounded.
    const expectedPct = Math.round((2 / CUTOVER_STEPS.length) * 100)
    expect(screen.getByTestId('cutover-progress-pct').textContent).toMatch(
      new RegExp(`${expectedPct}%`),
    )
    expect(
      screen
        .getByTestId('cutover-step-gitea-mirror')
        .getAttribute('data-step-status'),
    ).toBe('done')
    expect(
      screen
        .getByTestId('cutover-step-harbor-prewarm')
        .getAttribute('data-step-status'),
    ).toBe('running')
    expect(
      screen
        .getByTestId('cutover-step-egress-block-test')
        .getAttribute('data-step-status'),
    ).toBe('pending')
  })
})

describe('CutoverProgressCard — failure surface', () => {
  it('renders the per-step error message inline', () => {
    const status: CutoverStatus = parseCutoverStatus({
      state: 'tethered',
      steps: [
        {
          step: 'gitea-mirror',
          status: 'failed',
          message: 'cannot push to local Gitea: 503',
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
    render(<CutoverProgressCard status={merged} />)
    expect(
      screen.getByTestId('cutover-step-gitea-mirror-error').textContent,
    ).toMatch(/Gitea: 503/)
  })

  it('renders the stream-level error banner above the rows', () => {
    render(
      <CutoverProgressCard
        status={buildInitialCutoverStatus()}
        streamError="SSE dropped repeatedly"
      />,
    )
    expect(screen.getByTestId('cutover-stream-error').textContent).toMatch(
      /dropped/,
    )
  })
})

describe('CutoverProgressCard — terminal sovereignty achieved', () => {
  it('renders the achieved summary ONLY when state=sovereign', () => {
    const inFlight = buildInitialCutoverStatus()
    const { rerender } = render(<CutoverProgressCard status={inFlight} />)
    expect(screen.queryByTestId('cutover-achieved-summary')).toBeNull()

    const terminal: CutoverStatus = parseCutoverStatus({
      state: 'sovereign',
      steps: CUTOVER_STEPS.map((s) => ({
        step: s.id,
        status: 'done',
        finishedAt: '2026-05-04T10:01:00Z',
      })),
      mirroredCommitSHA: 'cafef00d1234abcd',
      harborProjectCount: 7,
      egressTestPassed: true,
    })
    rerender(<CutoverProgressCard status={terminal} />)
    expect(screen.getByTestId('cutover-achieved-summary')).toBeTruthy()
    expect(screen.getByTestId('cutover-progress-pct').textContent).toMatch(
      /100%/,
    )
    expect(
      screen.getByTestId('cutover-summary-mirrored-sha').textContent,
    ).toMatch(/cafef00d1234/)
    expect(
      screen.getByTestId('cutover-summary-harbor-projects').textContent,
    ).toMatch(/7/)
    expect(screen.getByTestId('cutover-summary-egress').textContent).toMatch(
      /passed/,
    )
  })
})

describe('CutoverProgressCard — #5391 settled-roll override audit banner', () => {
  it('is absent when no override was used', () => {
    render(<CutoverProgressCard status={buildInitialCutoverStatus()} />)
    expect(screen.queryByTestId('cutover-settled-roll-overrides')).toBeNull()
  })

  it('names every excluded HelmRelease while the cutover is in flight', () => {
    const status: CutoverStatus = parseCutoverStatus({
      cutoverComplete: false,
      settledRollOverrides:
        'delta-corp/bp-keycloak=quota-wedged plan-quota #5393\ndelta-corp/bp-wordpress-tenant=dependency of bp-keycloak #5393',
      steps: [{ name: 'harbor-prewarm', result: 'success' }],
    })
    render(<CutoverProgressCard status={status} />)
    const banner = screen.getByTestId('cutover-settled-roll-overrides')
    expect(banner.textContent).toMatch(/named override/)
    expect(banner.textContent).toMatch(/delta-corp\/bp-keycloak=quota-wedged/)
    expect(banner.textContent).toMatch(/delta-corp\/bp-wordpress-tenant=/)
  })

  it('stays visible after sovereignty is achieved — the audit trail must not vanish on success', () => {
    const terminal: CutoverStatus = parseCutoverStatus({
      state: 'sovereign',
      cutoverComplete: true,
      settledRollOverrides:
        'delta-corp/bp-keycloak=quota-wedged plan-quota #5393',
      steps: CUTOVER_STEPS.map((s) => ({
        step: s.id,
        status: 'done',
        finishedAt: '2026-08-05T10:01:00Z',
      })),
    })
    render(<CutoverProgressCard status={terminal} />)
    expect(screen.getByTestId('cutover-achieved-summary')).toBeTruthy()
    expect(
      screen.getByTestId('cutover-settled-roll-overrides').textContent,
    ).toMatch(/bp-keycloak/)
  })
})

describe('CutoverProgressCard — the rows are the steps of the status response', () => {
  const rowIds = (c: HTMLElement) =>
    [...c.querySelectorAll('li[data-step-status]')].map((li) => li.getAttribute('data-testid'))

  it('renders six rows, in the order they run, for a six-step response', () => {
    // The API lists step names alphabetically.
    const names = ['egress-block-test', 'harbor-prewarm', 'ownership-pivot', 'parent-release', 'registry-pivot', 'upstream-flip']
    const status = parseCutoverStatus({
      totalSteps: 6,
      steps: names.map((name) => ({ name, result: name === 'harbor-prewarm' ? 'success' : '' })),
    })
    const { container } = render(<CutoverProgressCard status={status} />)
    expect(rowIds(container)).toEqual(
      ['harbor-prewarm', 'ownership-pivot', 'upstream-flip', 'registry-pivot', 'parent-release', 'egress-block-test'].map((n) => `cutover-step-${n}`),
    )
    expect(screen.getByText('Release the parent')).toBeTruthy()
    expect(screen.getByTestId('cutover-progress-pct').textContent).toBe('17%')
  })

  it('renders the same eleven rows, in the same order, for the eleven-step response', () => {
    const ids = CUTOVER_STEPS.map((s) => s.id)
    const status = parseCutoverStatus({
      cutoverComplete: true,
      steps: [...ids].sort().map((name) => ({ name, result: 'success' })),
    })
    const { container } = render(<CutoverProgressCard status={status} />)
    expect(rowIds(container)).toEqual(ids.map((id) => `cutover-step-${id}`))
  })

  it('renders an unknown step with its name as the title and counts against totalSteps', () => {
    const status = parseCutoverStatus({
      totalSteps: 11,
      steps: [{ name: 'gitea-mirror', result: 'success' }, { name: 'future-step', result: 'running' }],
    })
    const { container } = render(<CutoverProgressCard status={status} />)
    expect(rowIds(container)).toEqual(['cutover-step-gitea-mirror', 'cutover-step-future-step'])
    expect(screen.getByText('future-step')).toBeTruthy()
    expect(screen.getByTestId('cutover-progress-pct').textContent).toBe('9%')
  })
})

describe('CutoverProgressCard — a failed step is repeated a bounded number of times (SOV-242)', () => {
  const failed = (raw: Record<string, string>) =>
    parseCutoverStatus({ failedStep: 'egress-block-test', steps: [{ name: 'egress-block-test', result: 'failed' }], raw })

  it('shows the pending automatic repetition and offers no resume until the budget is spent', () => {
    const { rerender } = render(<CutoverProgressCard status={failed({ autoRetryStep: 'egress-block-test', autoRetryCount: '1', autoRetryLimit: '3' })} onResume={() => undefined} />)
    expect(screen.getByTestId('cutover-auto-retry').textContent).toMatch(/repetition 2 of 3/)
    expect(screen.queryByTestId('cutover-resume-button')).toBeNull()
    // The 3rd repetition runs: its step-started frame follows the seed that cleared failedStep.
    const third = failed({ autoRetryStep: 'egress-block-test', autoRetryCount: '3', autoRetryLimit: '3' })
    rerender(<CutoverProgressCard status={applyCutoverStepUpdate(third, { step: 'egress-block-test', status: 'running' })} onResume={() => undefined} />)
    expect(screen.queryByTestId('cutover-stopped') ?? screen.queryByTestId('cutover-auto-retry')).toBeNull()
    // The 3rd repetition failed; the next pass has not marked the record halted yet.
    rerender(<CutoverProgressCard status={third} onResume={() => undefined} />)
    expect(screen.queryByTestId('cutover-auto-retry')).toBeNull()
    expect(screen.getByTestId('cutover-stopped').textContent).toMatch(/after 3 automatic repetitions/)
    expect(screen.getByTestId('cutover-resume-button')).toBeTruthy()
  })

  it('names the step when the budget is spent, and the resume calls the start route', () => {
    let resumed = 0
    // The flat status ConfigMap frame of the event stream.
    const status = parseCutoverStatusConfigMap({
      failedStep: 'egress-block-test', 'step.egress-block-test.result': 'failed',
      autoRetryStep: 'egress-block-test', autoRetryCount: '3', autoRetryLimit: '3', autoRetryHalted: 'true',
    })
    render(<CutoverProgressCard status={status!} onResume={() => (resumed += 1)} />)
    expect(screen.getByTestId('cutover-stopped').textContent).toMatch(/Egress-block self-test.*after 3 automatic repetitions/)
    fireEvent.click(screen.getByTestId('cutover-resume-button'))
    expect(resumed).toBe(1)
  })

  it('ignores a record about another step: no repetition is scheduled yet, so it says so, not stopped', () => {
    const status = failed({ autoRetryStep: 'harbor-prewarm', autoRetryCount: '3', autoRetryHalted: 'true' })
    expect(status.autoRetry).toBeUndefined()
    render(<CutoverProgressCard status={status} onResume={() => undefined} />)
    expect(screen.getByTestId('cutover-auto-retry').textContent).toMatch(/No automatic repetition is scheduled yet/)
    expect(screen.queryByTestId('cutover-stopped')).toBeNull()
    // A dormant reconciler never opens a record: Resume stays offered.
    expect(screen.getByTestId('cutover-resume-button')).toBeTruthy()
  })

  it('reads no repetition record without its count and limit, never 0 of 0', () => {
    const status = failed({ autoRetryStep: 'egress-block-test', autoRetryCount: '3', autoRetryHalted: 'true' })
    expect(status.autoRetry).toBeUndefined()
    render(<CutoverProgressCard status={status} onResume={() => undefined} />)
    expect(screen.queryByText(/after 0 automatic repetitions/)).toBeNull()
  })
})

describe('SovereigntyCard — the stop and Resume cutover follow failedStep and the halt record, not a failed row', () => {
  // The engine fails these two without a failed row: secondary-kubeconfigs writes no step keys, and the
  // gate after the chain leaves step.registry-pivot.result at success. The done row hides the Start button.
  it.each([
    ['secondary-kubeconfigs', 'secondary-kubeconfigs', {}],
    ['registry-pivot', 'Pivot containerd registries', { 'step.registry-pivot.result': 'success' }],
  ])('%s halted: names the step and Resume cutover calls the start route', (step, label, rows) => {
    const startCutover = vi.fn().mockResolvedValue(undefined)
    const status = parseCutoverStatusConfigMap({ ...rows, 'step.gitea-mirror.result': 'success', failedStep: step,
      autoRetryStep: step, autoRetryCount: '3', autoRetryLimit: '3', autoRetryHalted: 'true' })!
    render(<SovereigntyCard override={{ status, streamStatus: 'streaming', streamError: null, startCutover, retry: vi.fn(), starting: false, startError: 'refused' }} />)
    expect(screen.getByTestId('cutover-stopped').textContent).toMatch(new RegExp(`“${label}”.*after 3 automatic repetitions.*refused`))
    fireEvent.click(screen.getByTestId('cutover-resume-button'))
    expect(startCutover).toHaveBeenCalledTimes(1)
  })

  it('secondary-kubeconfigs failed before any step row: names the stopped step and Resume cutover calls the start route', () => {
    const startCutover = vi.fn().mockResolvedValue(undefined)
    const status = parseCutoverStatusConfigMap({ failedStep: 'secondary-kubeconfigs',
      autoRetryStep: 'secondary-kubeconfigs', autoRetryCount: '3', autoRetryLimit: '3', autoRetryHalted: 'true' })!
    expect(status.steps.every((s) => s.status === 'pending')).toBe(true)
    render(<SovereigntyCard override={{ status, streamStatus: 'streaming', streamError: null, startCutover, retry: vi.fn(), starting: false, startError: null }} />)
    expect(screen.getByTestId('cutover-stopped').textContent).toMatch(/“secondary-kubeconfigs”.*after 3 automatic repetitions/)
    fireEvent.click(screen.getByTestId('cutover-resume-button'))
    expect(startCutover).toHaveBeenCalledTimes(1)
  })
})
