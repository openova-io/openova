/**
 * FloatingLogPane.test.tsx — coverage for the slide-in 25vw log
 * viewer. Isolated from the FlowPage so the component contract is
 * lockable independent of canvas state.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { FloatingLogPane } from './FloatingLogPane'

afterEach(() => cleanup())

function renderPane(props: Partial<Parameters<typeof FloatingLogPane>[0]> = {}) {
  const onClose = props.onClose ?? vi.fn()
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  const r = render(
    <QueryClientProvider client={qc}>
      <FloatingLogPane
        executionId={'executionId' in props ? props.executionId : 'job-x:latest'}
        jobTitle={props.jobTitle ?? 'Install Cilium'}
        statusLabel={props.statusLabel ?? 'Running'}
        statusTone={props.statusTone ?? 'running'}
        onClose={onClose}
      />
    </QueryClientProvider>,
  )
  return { ...r, onClose }
}

describe('FloatingLogPane — render', () => {
  it('renders the floating-log-pane testid and the job title', () => {
    renderPane({ jobTitle: 'Install Cilium' })
    expect(screen.queryByTestId('floating-log-pane')).toBeTruthy()
    expect(screen.queryByTestId('floating-log-pane-title')?.textContent).toBe('Install Cilium')
  })

  it('renders ExecutionLogs body when executionId is non-empty', () => {
    renderPane({ executionId: 'job-x:latest' })
    expect(screen.queryByTestId('floating-log-pane-body')).toBeTruthy()
    expect(screen.queryByTestId('floating-log-pane-empty')).toBeNull()
  })

  it('renders the empty-state when executionId is null/empty', () => {
    renderPane({ executionId: null })
    expect(screen.queryByTestId('floating-log-pane-empty')).toBeTruthy()
    const empty = screen.getByTestId('floating-log-pane-empty')
    expect((empty.textContent ?? '').toLowerCase()).toContain('no execution')
  })

  it('inline width is 25vw', () => {
    renderPane()
    const aside = screen.getByTestId('floating-log-pane') as HTMLElement
    // jsdom keeps the inline `style.width` verbatim from React's
    // CSSProperties — assert the literal value (not a computed
    // resolution which jsdom does not perform for vw units).
    expect(aside.style.width).toBe('25vw')
  })
})

describe('FloatingLogPane — close behaviour', () => {
  it('clicking the X button calls onClose', () => {
    const { onClose } = renderPane()
    fireEvent.click(screen.getByTestId('floating-log-pane-close'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('pressing Escape calls onClose', () => {
    const { onClose } = renderPane()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('removes the document Escape listener on unmount', () => {
    const onClose = vi.fn()
    const { unmount } = renderPane({ onClose })
    unmount()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).not.toHaveBeenCalled()
  })
})
