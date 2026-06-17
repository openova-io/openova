/**
 * TopologyEditor.test.tsx — unit tests for EPIC-2 slice T (#1097)
 * topology editor widget. Uses disableNetwork seam so no fetch is
 * required.
 *
 * One vocabulary (#3375 DoD-1): the editor renders + selects the four
 * CANONICAL classes (singleton / active-active / active-hot-standby /
 * active-passive). These tests deliberately feed LEGACY-spelled
 * `currentMode` / `placementSchema.modes` props to prove the editor
 * folds them onto the canonical token (pre-selects the right radio,
 * intersects allowedModes correctly).
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
  it('renders the four canonical modes; a legacy currentMode pre-selects the canonical radio', () => {
    render(
      withProviders(
        <TopologyEditor
          sovereignId="dep-1"
          applicationName="wp-prod"
          // Legacy spelling on the wire …
          currentMode="single-region"
          currentRegions={['hz-fsn-rtz-prod']}
          availableRegions={['hz-fsn-rtz-prod', 'hz-hel-rtz-prod']}
          disableNetwork
        />,
      ),
    )
    // … folds to the canonical singleton radio, which is pre-selected.
    const singleton = screen.getByTestId('topology-editor-mode-singleton-radio') as HTMLInputElement
    expect(singleton.checked).toBe(true)
    // All four canonical classes are representable.
    expect(screen.getByTestId('topology-editor-mode-active-active-radio')).toBeTruthy()
    expect(screen.getByTestId('topology-editor-mode-active-hot-standby-radio')).toBeTruthy()
    expect(screen.getByTestId('topology-editor-mode-active-passive-radio')).toBeTruthy()
  })

  it('switching to active-hot-standby allows multi-region selection', () => {
    render(
      withProviders(
        <TopologyEditor
          sovereignId="dep-1"
          applicationName="wp-prod"
          currentMode="singleton"
          currentRegions={['hz-fsn-rtz-prod']}
          availableRegions={['hz-fsn-rtz-prod', 'hz-hel-rtz-prod']}
          disableNetwork
        />,
      ),
    )
    fireEvent.click(screen.getByTestId('topology-editor-mode-active-hot-standby-radio'))
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
          currentMode="active-hot-standby"
          currentRegions={['hz-fsn-rtz-prod']}
          availableRegions={['hz-fsn-rtz-prod', 'hz-hel-rtz-prod']}
          supportedCanonical={['singleton', 'active-hot-standby']}
          disableNetwork
        />,
      ),
    )
    // active-hot-standby is supported → enabled
    const hot = screen.getByTestId('topology-editor-mode-active-hot-standby-radio') as HTMLInputElement
    expect(hot.disabled).toBe(false)
    // singleton is supported → enabled
    const singleton = screen.getByTestId('topology-editor-mode-singleton-radio') as HTMLInputElement
    expect(singleton.disabled).toBe(false)
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
          currentMode="singleton"
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
  it('disables modes the Blueprint placementSchema does not allow (legacy modes[] folded)', () => {
    render(
      withProviders(
        <TopologyEditor
          sovereignId="dep-1"
          applicationName="wp-prod"
          currentMode="singleton"
          currentRegions={['a']}
          availableRegions={['a', 'b']}
          blueprint={{
            name: 'bp-x',
            version: '1.0.0',
            card: { title: 'X' },
            origin: 1,
            source: 'public',
            // Legacy spelling in modes[] still constrains the canonical
            // picker — active-active is not allowed.
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
