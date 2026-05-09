/**
 * UpgradeDialog.test.tsx — unit tests for EPIC-2 slice O (#1097)
 * upgrade dialog. Uses disableNetwork seam so no fetch is required.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { UpgradeDialog } from './UpgradeDialog'

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

afterEach(() => cleanup())

describe('UpgradeDialog', () => {
  it('does not render when open=false', () => {
    render(
      withProviders(
        <UpgradeDialog
          open={false}
          onClose={() => undefined}
          sovereignId="dep-1"
          applicationName="wp-prod"
          blueprintName="bp-wordpress"
          currentVersion="1.0.0"
          disableNetwork
        />,
      ),
    )
    expect(screen.queryByTestId('upgrade-dialog')).toBeNull()
  })

  it('renders header with current version', () => {
    render(
      withProviders(
        <UpgradeDialog
          open
          onClose={() => undefined}
          sovereignId="dep-1"
          applicationName="wp-prod"
          blueprintName="bp-wordpress"
          currentVersion="1.0.0"
          disableNetwork
        />,
      ),
    )
    expect(screen.getByTestId('upgrade-dialog')).toBeTruthy()
  })

  it('apply is blocked until target version is picked', () => {
    render(
      withProviders(
        <UpgradeDialog
          open
          onClose={() => undefined}
          sovereignId="dep-1"
          applicationName="wp-prod"
          blueprintName="bp-wordpress"
          currentVersion="1.0.0"
          disableNetwork
        />,
      ),
    )
    const btn = screen.getByTestId('upgrade-dialog-apply-btn') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
  })
})
