/**
 * BatchProgress.test.tsx — render coverage for the per-batch progress
 * strip rendered above JobsTable (issue #204 founder spec item #4).
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { BatchProgress } from './BatchProgress'
import { FIXTURE_BATCHES, deriveBatches, FIXTURE_JOBS } from '@/test/fixtures/jobs.fixture'

afterEach(() => cleanup())

describe('BatchProgress', () => {
  it('renders nothing when no batches are supplied', () => {
    const { container } = render(<BatchProgress batches={[]} />)
    expect(container.firstChild).toBeNull()
  })

  it('renders one row per batch with the matching label', () => {
    render(<BatchProgress batches={FIXTURE_BATCHES} />)
    expect(screen.getByTestId('batch-row-batch-1')).toBeTruthy()
    expect(screen.getByTestId('batch-row-batch-2')).toBeTruthy()
    expect(screen.getByTestId('batch-label-batch-1').textContent).toBe('batch-1')
    expect(screen.getByTestId('batch-label-batch-2').textContent).toBe('batch-2')
  })

  it('renders the finished/total count for each batch', () => {
    render(<BatchProgress batches={FIXTURE_BATCHES} />)
    expect(screen.getByTestId('batch-count-batch-1').textContent).toBe('4/5')
    expect(screen.getByTestId('batch-count-batch-2').textContent).toBe('2/3')
  })

  it('exposes an aria progressbar with valuenow set to the percentage', () => {
    render(<BatchProgress batches={FIXTURE_BATCHES} />)
    const bars = screen.getAllByRole('progressbar')
    expect(bars.length).toBe(2)
    // batch-1: 4/5 = 80%, batch-2: 2/3 = 67%.
    const values = bars.map((b) => Number(b.getAttribute('aria-valuenow')))
    expect(values).toContain(80)
    expect(values).toContain(67)
  })

  it('shows the failed-chip for batches with at least one failed job', () => {
    render(<BatchProgress batches={FIXTURE_BATCHES} />)
    // batch-2 has failed=1; batch-1 does not.
    expect(screen.queryByTestId('batch-chip-failed-batch-2')).toBeTruthy()
    expect(screen.queryByTestId('batch-chip-failed-batch-1')).toBeNull()
  })

  it('deriveBatches matches the fixture rollups', () => {
    const derived = deriveBatches(FIXTURE_JOBS)
    expect(derived).toEqual(FIXTURE_BATCHES)
  })
})
