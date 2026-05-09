/**
 * UninstallDialog.test.tsx — unit tests for EPIC-2 slice O (#1097)
 * uninstall confirmation dialog.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { UninstallDialog } from './UninstallDialog'

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

afterEach(() => cleanup())

describe('UninstallDialog', () => {
  it('does not render when open=false', () => {
    render(
      withProviders(
        <UninstallDialog
          open={false}
          onClose={() => undefined}
          sovereignId="dep-1"
          applicationName="wp-prod"
          disableNetwork
        />,
      ),
    )
    expect(screen.queryByTestId('uninstall-dialog')).toBeNull()
  })

  it('blocks confirm until typed name matches', () => {
    render(
      withProviders(
        <UninstallDialog
          open
          onClose={() => undefined}
          sovereignId="dep-1"
          applicationName="wp-prod"
          disableNetwork
        />,
      ),
    )
    const btn = screen.getByTestId('uninstall-dialog-confirm') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    fireEvent.change(screen.getByTestId('uninstall-dialog-confirm-input'), {
      target: { value: 'wp-prod' },
    })
    expect((screen.getByTestId('uninstall-dialog-confirm') as HTMLButtonElement).disabled).toBe(false)
  })

  it('fires onUninstalled when confirmed (test seam)', () => {
    let called = false
    render(
      withProviders(
        <UninstallDialog
          open
          onClose={() => undefined}
          sovereignId="dep-1"
          applicationName="wp-prod"
          disableNetwork
          onUninstalled={() => {
            called = true
          }}
        />,
      ),
    )
    fireEvent.change(screen.getByTestId('uninstall-dialog-confirm-input'), {
      target: { value: 'wp-prod' },
    })
    fireEvent.click(screen.getByTestId('uninstall-dialog-confirm'))
    expect(called).toBe(true)
  })

  it('rejects mismatched name (case-sensitive)', () => {
    render(
      withProviders(
        <UninstallDialog
          open
          onClose={() => undefined}
          sovereignId="dep-1"
          applicationName="wp-prod"
          disableNetwork
        />,
      ),
    )
    fireEvent.change(screen.getByTestId('uninstall-dialog-confirm-input'), {
      target: { value: 'WP-PROD' },
    })
    expect((screen.getByTestId('uninstall-dialog-confirm') as HTMLButtonElement).disabled).toBe(true)
  })
})
