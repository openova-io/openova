/**
 * TopologyEditor.test.tsx — unit tests for EPIC-2 slice T (#1097)
 * topology editor widget. Uses disableNetwork seam so no fetch is
 * required.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { TopologyEditor } from './TopologyEditor'

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

afterEach(() => cleanup())

describe('TopologyEditor — mode picker', () => {
  it('renders all three modes with current mode pre-selected', () => {
    render(
      withProviders(
        <TopologyEditor
          sovereignId="dep-1"
          applicationName="wp-prod"
          currentMode="single-region"
          currentRegions={['hz-fsn-rtz-prod']}
          availableRegions={['hz-fsn-rtz-prod', 'hz-hel-rtz-prod']}
          disableNetwork
        />,
      ),
    )
    const single = screen.getByTestId('topology-editor-mode-single-region-radio') as HTMLInputElement
    expect(single.checked).toBe(true)
  })

  it('switching to active-hotstandby allows multi-region selection', () => {
    render(
      withProviders(
        <TopologyEditor
          sovereignId="dep-1"
          applicationName="wp-prod"
          currentMode="single-region"
          currentRegions={['hz-fsn-rtz-prod']}
          availableRegions={['hz-fsn-rtz-prod', 'hz-hel-rtz-prod']}
          disableNetwork
        />,
      ),
    )
    fireEvent.click(screen.getByTestId('topology-editor-mode-active-hotstandby-radio'))
    fireEvent.click(screen.getByTestId('topology-editor-region-hz-hel-rtz-prod-checkbox'))
    const apply = screen.getByTestId('topology-editor-apply-btn') as HTMLButtonElement
    expect(apply.disabled).toBe(false)
  })

  it('constrains the picker to the Blueprint supported topologies (#3648)', () => {
    render(
      withProviders(
        <TopologyEditor
          sovereignId="dep-1"
          applicationName="grafana"
          currentMode="active-hotstandby"
          currentRegions={['hz-fsn-rtz-prod']}
          availableRegions={['hz-fsn-rtz-prod', 'hz-hel-rtz-prod']}
          supportedCanonical={['singleton', 'active-hot-standby']}
          disableNetwork
        />,
      ),
    )
    // active-hotstandby canonicalises to active-hot-standby → supported → enabled
    const hot = screen.getByTestId('topology-editor-mode-active-hotstandby-radio') as HTMLInputElement
    expect(hot.disabled).toBe(false)
    // single-region canonicalises to singleton → supported → enabled
    const single = screen.getByTestId('topology-editor-mode-single-region-radio') as HTMLInputElement
    expect(single.disabled).toBe(false)
    // active-active is NOT in the Blueprint's supported set → disabled (the
    // contradiction the operator flagged 3×: never offer an unsupported mode).
    const aa = screen.getByTestId('topology-editor-mode-active-active-radio') as HTMLInputElement
    expect(aa.disabled).toBe(true)
  })
})

describe('TopologyEditor — destructive transitions', () => {
  it('shows force-confirm warning when scaling DOWN regions', () => {
    render(
      withProviders(
        <TopologyEditor
          sovereignId="dep-1"
          applicationName="wp-prod"
          currentMode="active-active"
          currentRegions={['a', 'b', 'c']}
          availableRegions={['a', 'b', 'c']}
          disableNetwork
        />,
      ),
    )
    // Drop region c.
    fireEvent.click(screen.getByTestId('topology-editor-region-c-checkbox'))
    expect(screen.getByTestId('topology-editor-force-warning')).toBeTruthy()
    const apply = screen.getByTestId('topology-editor-apply-btn') as HTMLButtonElement
    expect(apply.disabled).toBe(true)
    fireEvent.click(screen.getByTestId('topology-editor-force-confirm'))
    expect((screen.getByTestId('topology-editor-apply-btn') as HTMLButtonElement).disabled).toBe(false)
  })
})

describe('TopologyEditor — preview', () => {
  it('renders preview modal with stub manifests when network is disabled', () => {
    render(
      withProviders(
        <TopologyEditor
          sovereignId="dep-1"
          applicationName="wp-prod"
          currentMode="single-region"
          currentRegions={['hz-fsn-rtz-prod']}
          availableRegions={['hz-fsn-rtz-prod', 'hz-hel-rtz-prod']}
          disableNetwork
        />,
      ),
    )
    fireEvent.click(screen.getByTestId('topology-editor-region-hz-hel-rtz-prod-checkbox'))
    fireEvent.click(screen.getByTestId('topology-editor-preview-btn'))
    expect(screen.getByTestId('topology-editor-preview-modal')).toBeTruthy()
  })
})

describe('TopologyEditor — Blueprint constraint', () => {
  it('disables modes the Blueprint placementSchema does not allow', () => {
    render(
      withProviders(
        <TopologyEditor
          sovereignId="dep-1"
          applicationName="wp-prod"
          currentMode="single-region"
          currentRegions={['a']}
          availableRegions={['a', 'b']}
          blueprint={{
            name: 'bp-x',
            version: '1.0.0',
            card: { title: 'X' },
            origin: 1,
            source: 'public',
            placementSchema: { modes: ['single-region'] },
          }}
          disableNetwork
        />,
      ),
    )
    const aaRadio = screen.getByTestId('topology-editor-mode-active-active-radio') as HTMLInputElement
    expect(aaRadio.disabled).toBe(true)
  })
})
