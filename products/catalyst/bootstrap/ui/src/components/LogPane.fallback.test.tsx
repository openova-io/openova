/**
 * LogPane — Bug #481 derived-job fallback log lines test.
 *
 * The JobDetail page used to render the "No execution recorded yet"
 * empty state for any job whose Bridge had not allocated an Execution
 * row — including all Phase-0 tofu jobs and `cluster-bootstrap`, which
 * live entirely in the SSE event reducer. Operators saw a blank pane
 * even though the events were already buffered client-side.
 *
 * The fix adds a `fallbackLines` prop to LogPane: when `executionId`
 * is null AND `fallbackLines` is non-empty, the pane renders those
 * lines through the same dark-theme, line-numbered presentation as
 * ExecutionLogs without polling. This test locks in the contract.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { LogPane } from './LogPane'
import type { LogLine } from './ExecutionLogs'

afterEach(() => cleanup())

const SAMPLE_LINES: LogLine[] = [
  {
    lineNumber: 1,
    timestamp: '2026-05-01T18:21:58Z',
    level: 'INFO',
    message: 'Initialising OpenTofu working directory',
  },
  {
    lineNumber: 2,
    timestamp: '2026-05-01T18:21:59Z',
    level: 'INFO',
    message: 'Initializing the backend...',
  },
  {
    lineNumber: 3,
    timestamp: '2026-05-01T18:22:02Z',
    level: 'INFO',
    message: 'OpenTofu has been successfully initialized!',
  },
]

describe('LogPane — Bug #481 fallbackLines path', () => {
  it('renders fallback lines when executionId is null and fallbackLines is non-empty', () => {
    render(
      <LogPane
        executionId={null}
        fallbackLines={SAMPLE_LINES}
        jobTitle="Provision infrastructure — terraform init"
        statusLabel="Succeeded"
        statusTone="succeeded"
        onClose={() => {}}
      />,
    )
    // The fallback list renders, NOT the empty state.
    const list = screen.queryByTestId('fallback-log-list')
    expect(list).not.toBeNull()
    expect(screen.queryByTestId('log-pane-empty')).toBeNull()
    // Every line is present, with its own row.
    expect(screen.getByTestId('fallback-log-line-1')).toBeTruthy()
    expect(screen.getByTestId('fallback-log-line-2')).toBeTruthy()
    expect(screen.getByTestId('fallback-log-line-3')).toBeTruthy()
    // Message text is visible.
    expect(
      screen.getByText('Initialising OpenTofu working directory'),
    ).toBeTruthy()
  })

  it('renders the empty state when executionId is null and fallbackLines is empty', () => {
    render(
      <LogPane
        executionId={null}
        fallbackLines={[]}
        jobTitle="Pending job"
        statusTone="pending"
        onClose={() => {}}
      />,
    )
    expect(screen.getByTestId('log-pane-empty')).toBeTruthy()
    expect(screen.queryByTestId('fallback-log-list')).toBeNull()
  })

  it('renders the empty state when both executionId and fallbackLines are absent', () => {
    render(
      <LogPane
        executionId={null}
        jobTitle="Pending job"
        statusTone="pending"
        onClose={() => {}}
      />,
    )
    expect(screen.getByTestId('log-pane-empty')).toBeTruthy()
  })
})
