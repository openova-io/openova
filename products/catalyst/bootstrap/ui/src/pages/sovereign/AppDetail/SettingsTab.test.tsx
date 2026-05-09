/**
 * SettingsTab.test.tsx — unit tests for EPIC-2 slice O (#1097)
 * SettingsTab. Uses initialApp + disableNetwork seams to bypass
 * fetches.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { SettingsTab } from './SettingsTab'

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

const sampleApp = {
  name: 'wp-prod',
  namespace: 'acme',
  phase: 'Ready',
  spec: {
    blueprintRef: { name: 'bp-wordpress', version: '1.2.3' },
    parameters: { domain: 'shop.acme.com' },
    placement: 'single-region',
    regions: ['hz-fsn-rtz-prod'],
    environmentRef: 'acme-prod',
  },
}

afterEach(() => cleanup())

describe('SettingsTab', () => {
  it('renders all 3 sub-sections (parameters / upgrade / danger)', () => {
    render(
      withProviders(
        <SettingsTab
          sovereignId="dep-1"
          applicationName="wp-prod"
          namespace="acme"
          initialApp={sampleApp}
          disableNetwork
        />,
      ),
    )
    expect(screen.getByTestId('app-settings-tab')).toBeTruthy()
    expect(screen.getByTestId('settings-tab-upgrade-btn')).toBeTruthy()
    expect(screen.getByTestId('settings-tab-uninstall-btn')).toBeTruthy()
  })

  it('opens uninstall dialog on click', () => {
    render(
      withProviders(
        <SettingsTab
          sovereignId="dep-1"
          applicationName="wp-prod"
          namespace="acme"
          initialApp={sampleApp}
          disableNetwork
        />,
      ),
    )
    fireEvent.click(screen.getByTestId('settings-tab-uninstall-btn'))
    expect(screen.getByTestId('uninstall-dialog')).toBeTruthy()
  })

  it('opens upgrade dialog on click', () => {
    render(
      withProviders(
        <SettingsTab
          sovereignId="dep-1"
          applicationName="wp-prod"
          namespace="acme"
          initialApp={sampleApp}
          disableNetwork
        />,
      ),
    )
    fireEvent.click(screen.getByTestId('settings-tab-upgrade-btn'))
    expect(screen.getByTestId('upgrade-dialog')).toBeTruthy()
  })
})
