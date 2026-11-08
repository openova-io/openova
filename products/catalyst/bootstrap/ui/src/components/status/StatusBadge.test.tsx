/**
 * StatusBadge.test.tsx — ADR-0015 D7: the shared status renderer prints the
 * API's statusOf(object) values VERBATIM and never invents a state.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { StatusBadge, StatusReason, StatusLine, RegionStatusList } from './StatusBadge'

afterEach(() => cleanup())

const SOVEREIGN_TEST = {
  state: 'NotReady',
  reason: 'EnvironmentMissing',
  message: 'Environment "sovereign-prod" not found',
}

describe('StatusBadge / StatusReason', () => {
  it('renders the sovereign/test state, reason and message verbatim', () => {
    render(<StatusLine status={SOVEREIGN_TEST} testId="s" />)
    expect(screen.getByTestId('s-badge').textContent).toBe('NotReady')
    expect(screen.getByTestId('s-badge').getAttribute('data-state')).toBe('NotReady')
    expect(screen.getByTestId('s-reason-reason').textContent).toBe('EnvironmentMissing')
    expect(screen.getByTestId('s-reason-message').textContent).toBe('Environment "sovereign-prod" not found')
  })

  it('an unrecognised state string is printed as-is, not remapped', () => {
    render(<StatusBadge status={{ state: 'Degraded' }} testId="b" />)
    expect(screen.getByTestId('b').textContent).toBe('Degraded')
  })

  it('absent status renders Unknown — never Pending / Healthy', () => {
    render(<StatusBadge status={undefined} testId="b" />)
    expect(screen.getByTestId('b').textContent).toBe('Unknown')
    cleanup()
    render(<StatusBadge status={{ state: '' }} testId="b" />)
    expect(screen.getByTestId('b').textContent).toBe('Unknown')
  })

  it('StatusReason renders nothing when there is no reason or message', () => {
    const { container } = render(<StatusReason status={{ state: 'Ready' }} />)
    expect(container.textContent).toBe('')
  })

  it('blocked-sync WordPress: per-region Serving=False renders verbatim per region', () => {
    render(
      <RegionStatusList
        testId="r"
        status={{
          state: 'NotReady',
          reason: 'SynchronousStandbyNotStreaming',
          perRegion: [
            { region: 'me-east-215-a', state: 'NotReady', reason: 'SynchronousStandbyNotStreaming', message: 'every commit blocks' },
            { region: 'me-east-215-b', state: 'Unknown', reason: 'ServingNotObservable', message: 'not observable from me-east-215-a' },
          ],
        }}
      />,
    )
    expect(screen.getByTestId('r-me-east-215-a').textContent).toContain(
      'NotReadySynchronousStandbyNotStreaming — every commit blocks',
    )
    expect(screen.getByTestId('r-me-east-215-b-line-badge').textContent).toBe('Unknown')
    expect(screen.getByTestId('r-me-east-215-b').textContent).toContain('ServingNotObservable — not observable from me-east-215-a')
  })
})
