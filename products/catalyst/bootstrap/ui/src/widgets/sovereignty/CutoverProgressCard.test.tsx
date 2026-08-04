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

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { CutoverProgressCard } from './CutoverProgressCard'
import {
  buildInitialCutoverStatus,
  CUTOVER_STEPS,
  parseCutoverStatus,
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
