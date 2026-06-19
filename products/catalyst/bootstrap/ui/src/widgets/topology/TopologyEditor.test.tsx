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

import { TopologyEditor, describeModeForComponent } from './TopologyEditor'
import { TOPOLOGY_BY_ID } from '@/shared/constants/catalog.generated'

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

describe('TopologyEditor — matrix-derived mode descriptions (#3905)', () => {
  // The exact bug the founder flagged: the card derived openbao's
  // active-passive as `raft · async` warm-standby from the topology matrix,
  // while the edit dropdown printed a GENERIC "…(backup-restore)" string for
  // every component. The dropdown now reads the SAME matrix, so its
  // active-passive helper text must mirror the matrix contract and never say
  // "backup-restore".
  const openbaoTopology = TOPOLOGY_BY_ID['bp-openbao']

  it('the openbao active-passive option derives raft/async/raft-transition from the matrix (no backup-restore)', () => {
    expect(openbaoTopology).toBeTruthy()
    render(
      withProviders(
        <TopologyEditor
          sovereignId="dep-1"
          applicationName="openbao"
          currentMode="active-passive"
          currentRegions={['mgmt-A']}
          availableRegions={['mgmt-A', 'mgmt-B']}
          supportedCanonical={openbaoTopology.supported}
          topology={openbaoTopology}
          disableNetwork
        />,
      ),
    )
    const desc = screen.getByTestId('topology-editor-mode-active-passive-desc')
    const text = (desc.textContent ?? '').toLowerCase()
    // Matches the CARD's matrix-derived semantics (raft · async + raft-transition + 60s/30s).
    expect(text).toContain('raft')
    expect(text).toContain('async')
    expect(text).toContain('raft-transition')
    expect(text).toContain('60s')
    expect(text).toContain('30s')
    // The contradiction is gone: no generic "backup-restore" anywhere.
    expect(text).not.toContain('backup-restore')
  })

  it('describeModeForComponent for openbao active-passive exactly matches the matrix variant', () => {
    const text = describeModeForComponent('active-passive', openbaoTopology).toLowerCase()
    const v = openbaoTopology.perTopology['active-passive']
    // Every field the dropdown shows is sourced from the matrix, not hardcoded.
    expect(v?.replication?.backend).toBe('raft')
    expect(v?.replication?.mode).toBe('async')
    expect(v?.switchover?.mechanism).toBe('raft-transition')
    expect(text).toContain(`${v!.replication!.backend} · ${v!.replication!.mode}`.toLowerCase())
    expect(text).toContain('raft-transition switchover')
    expect(text).not.toContain('backup-restore')
  })

  it('falls back to the generic one-liner when no matrix is supplied', () => {
    // An app with no spec.topology block → no `topology` prop → generic copy.
    const text = describeModeForComponent('active-passive', undefined).toLowerCase()
    expect(text).toContain('promotes on failover')
    // The generic fallback is mechanism-neutral; it must not claim backup-restore.
    expect(text).not.toContain('backup-restore')
  })

  it('singleton uses the generic one-liner even with a matrix (no DR contract to derive)', () => {
    const text = describeModeForComponent('singleton', openbaoTopology).toLowerCase()
    expect(text).toContain('one cluster')
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
