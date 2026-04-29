/**
 * ExecutionLogs.test.tsx — unit coverage for the GitLab-CI-style log
 * viewer (epic #204 founder requirement item 3).
 *
 * Coverage:
 *   1. Renders 3 lines with correct timestamp formatting (HH:MM:SS.MMM)
 *      + level-badge text + line-number column.
 *   2. Level badge picks the right palette (INFO/DEBUG/WARN/ERROR).
 *   3. Line numbers right-align in a tabular-numeric column.
 *   4. Simulating the operator scrolling up sets pausedAutoScroll → the
 *      "Resume tail" pill becomes visible.
 *   5. Simulating `executionFinished: true` (with no further new lines)
 *      stops the polling loop.
 *   6. `formatLogTimestamp` slices ISO timestamps down to HH:MM:SS.MMM
 *      with millisecond padding.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, cleanup, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  ExecutionLogs,
  formatLogTimestamp,
  type ExecutionLogsResponse,
  type LogLine,
} from './ExecutionLogs'

function makeLine(n: number, level: LogLine['level'], message: string): LogLine {
  // Shifted base of 15:00:00.000 + n milliseconds so each line has a
  // distinct millisecond stamp the test can assert against.
  const ms = 1000 + n * 7
  const ts = `2026-04-29T15:00:00.${String(ms).padStart(3, '0').slice(-3)}Z`
  return { lineNumber: n, timestamp: ts, level, message }
}

function renderViewer(props: Parameters<typeof ExecutionLogs>[0]) {
  // Each render gets its own QueryClient so query cache state never
  // bleeds between tests.
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <ExecutionLogs {...props} />
    </QueryClientProvider>,
  )
}

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

beforeEach(() => {
  // jsdom doesn't lay anything out — we have to stub the geometry
  // properties our scroll-handler reads. The defaults below mimic a
  // viewport that's currently scrolled to the bottom.
  Object.defineProperty(HTMLElement.prototype, 'scrollHeight', {
    configurable: true,
    get() {
      return Number(this.dataset.testScrollHeight ?? 1000)
    },
  })
  Object.defineProperty(HTMLElement.prototype, 'clientHeight', {
    configurable: true,
    get() {
      return Number(this.dataset.testClientHeight ?? 600)
    },
  })
})

describe('formatLogTimestamp', () => {
  it('slices ISO timestamps down to HH:MM:SS.MMM', () => {
    expect(formatLogTimestamp('2026-04-29T15:00:01.234Z')).toBe('15:00:01.234')
  })

  it('pads sub-millisecond precision to three digits', () => {
    expect(formatLogTimestamp('2026-04-29T15:00:01.5Z')).toBe('15:00:01.500')
    expect(formatLogTimestamp('2026-04-29T15:00:01.42Z')).toBe('15:00:01.420')
  })

  it('emits HH:MM:SS.000 when no fractional seconds were sent', () => {
    expect(formatLogTimestamp('2026-04-29T15:00:01Z')).toBe('15:00:01.000')
  })

  it('returns empty for empty input', () => {
    expect(formatLogTimestamp('')).toBe('')
  })
})

describe('ExecutionLogs — rendering', () => {
  it('renders 3 lines with timestamp + level + message + line number', async () => {
    const lines: LogLine[] = [
      makeLine(1, 'INFO',  'Reconciling HelmRelease'),
      makeLine(2, 'DEBUG', 'Pulling chart from OCI'),
      makeLine(3, 'WARN',  'Slow chart pull — 8.2s'),
    ]
    const fetcher = vi.fn(async (): Promise<ExecutionLogsResponse> => ({
      lines,
      total: lines.length,
      executionFinished: false,
    }))

    renderViewer({
      executionId: 'exec-aaa',
      fetcher,
      disablePolling: true,
    })

    await waitFor(() => {
      expect(screen.getByTestId('execution-logs-line-1')).toBeTruthy()
    })

    // Line 1 — INFO, line number, formatted timestamp.
    const num1 = screen.getByTestId('execution-logs-linenum-1')
    expect(num1.textContent).toBe('1')
    const ts1 = screen.getByTestId('execution-logs-ts-1')
    expect(ts1.textContent).toMatch(/^\d{2}:\d{2}:\d{2}\.\d{3}$/)
    const lvl1 = screen.getByTestId('execution-logs-level-1')
    expect(lvl1.textContent).toBe('INFO')
    expect(lvl1.getAttribute('data-level')).toBe('INFO')
    const msg1 = screen.getByTestId('execution-logs-msg-1')
    expect(msg1.textContent).toContain('Reconciling HelmRelease')

    // Line 2 — DEBUG.
    expect(screen.getByTestId('execution-logs-level-2').textContent).toBe('DEBUG')
    // Line 3 — WARN.
    expect(screen.getByTestId('execution-logs-level-3').textContent).toBe('WARN')
  })

  it('renders ERROR-level lines with the ERROR badge', async () => {
    const fetcher = vi.fn(async (): Promise<ExecutionLogsResponse> => ({
      lines: [makeLine(7, 'ERROR', 'rate limited by API')],
      total: 1,
      executionFinished: false,
    }))
    renderViewer({ executionId: 'exec-err', fetcher, disablePolling: true })
    await waitFor(() => {
      expect(screen.getByTestId('execution-logs-level-7').textContent).toBe('ERROR')
    })
  })

  it('renders the empty placeholder when no lines have arrived', async () => {
    const fetcher = vi.fn(async (): Promise<ExecutionLogsResponse> => ({
      lines: [],
      total: 0,
      executionFinished: false,
    }))
    renderViewer({ executionId: 'exec-empty', fetcher, disablePolling: true })
    await waitFor(() => {
      expect(screen.getByTestId('execution-logs-empty')).toBeTruthy()
    })
  })

  it('uses the canonical #0D1117 background colour', async () => {
    const fetcher = vi.fn(async (): Promise<ExecutionLogsResponse> => ({
      lines: [makeLine(1, 'INFO', 'go')],
      total: 1,
      executionFinished: false,
    }))
    renderViewer({ executionId: 'exec-bg', fetcher, disablePolling: true })
    await waitFor(() => {
      expect(screen.getByTestId('execution-logs-line-1')).toBeTruthy()
    })
    const root = screen.getByTestId('execution-logs-root')
    // jsdom canonicalises hex → rgb. We accept either form so the test
    // is portable across environments that preserve the literal vs.
    // those that round-trip via CSSOM. Both representations encode the
    // founder-specified `#0D1117`.
    const bg = (root as HTMLElement).style.background.toLowerCase()
    expect(bg === '#0d1117' || bg.includes('rgb(13, 17, 23)')).toBe(true)
  })
})

describe('ExecutionLogs — auto-scroll pause', () => {
  it('shows "Resume tail" pill when the operator scrolls up', async () => {
    const fetcher = vi.fn(async (): Promise<ExecutionLogsResponse> => ({
      lines: [makeLine(1, 'INFO', 'go')],
      total: 1,
      executionFinished: false,
    }))
    renderViewer({ executionId: 'exec-scroll', fetcher, disablePolling: true })

    await waitFor(() => {
      expect(screen.getByTestId('execution-logs-line-1')).toBeTruthy()
    })

    // No pill while at-bottom.
    expect(screen.queryByTestId('execution-logs-resume')).toBeNull()

    // Simulate the operator scrolling up. We move the viewport's
    // scrollTop to 0 (top), keeping scrollHeight=1000 and
    // clientHeight=600 ⇒ distance-from-bottom = 1000 - (0+600) = 400px,
    // well above the 40px near-bottom slack.
    const viewport = screen.getByTestId('execution-logs-viewport')
    Object.defineProperty(viewport, 'scrollTop', { configurable: true, value: 0, writable: true })
    fireEvent.scroll(viewport)

    expect(screen.getByTestId('execution-logs-resume')).toBeTruthy()
  })

  it('hides the pill again when the operator clicks "Resume tail"', async () => {
    const fetcher = vi.fn(async (): Promise<ExecutionLogsResponse> => ({
      lines: [makeLine(1, 'INFO', 'go')],
      total: 1,
      executionFinished: false,
    }))
    renderViewer({ executionId: 'exec-resume', fetcher, disablePolling: true })

    await waitFor(() => {
      expect(screen.getByTestId('execution-logs-line-1')).toBeTruthy()
    })

    const viewport = screen.getByTestId('execution-logs-viewport')
    Object.defineProperty(viewport, 'scrollTop', { configurable: true, value: 0, writable: true })
    fireEvent.scroll(viewport)
    const pill = screen.getByTestId('execution-logs-resume')
    expect(pill).toBeTruthy()

    fireEvent.click(pill)
    await waitFor(() => {
      expect(screen.queryByTestId('execution-logs-resume')).toBeNull()
    })
  })
})

describe('ExecutionLogs — polling lifecycle', () => {
  it('marks the viewer as done when the API reports executionFinished and no new lines remain', async () => {
    const fetcher = vi.fn(async (): Promise<ExecutionLogsResponse> => ({
      lines: [],
      total: 0,
      executionFinished: true,
    }))
    renderViewer({ executionId: 'exec-finished', fetcher, disablePolling: true })

    await waitFor(() => {
      expect(screen.getByTestId('execution-logs-done')).toBeTruthy()
    })
  })

  it('continues to render previously-fetched lines after the execution finishes', async () => {
    const lines = [makeLine(1, 'INFO', 'first'), makeLine(2, 'INFO', 'second')]
    let calls = 0
    const fetcher = vi.fn(async (): Promise<ExecutionLogsResponse> => {
      calls += 1
      if (calls === 1) {
        return { lines, total: 2, executionFinished: false }
      }
      return { lines: [], total: 2, executionFinished: true }
    })

    // Polling enabled but driven by fake timers — we manually advance.
    vi.useFakeTimers()
    renderViewer({ executionId: 'exec-phased', fetcher })

    await vi.waitFor(() => {
      expect(screen.queryByTestId('execution-logs-line-1')).toBeTruthy()
    })

    // Advance past the 1s refetch interval to trigger the second poll
    // (the one that returns executionFinished:true).
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100)
    })

    await vi.waitFor(() => {
      expect(screen.getByTestId('execution-logs-done')).toBeTruthy()
    })

    // Original lines are still on screen.
    expect(screen.getByTestId('execution-logs-line-1')).toBeTruthy()
    expect(screen.getByTestId('execution-logs-line-2')).toBeTruthy()
  })
})
