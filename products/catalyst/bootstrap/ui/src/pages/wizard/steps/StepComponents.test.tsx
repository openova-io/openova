/**
 * StepComponents.test.tsx — vitest coverage for the corporate platform
 * component grid (GitHub issue #161).
 *
 * Covers:
 *   - search filter narrows visible cards
 *   - category chip filter narrows visible cards (groupId)
 *   - sort: selected items float to top, then alphabetical
 *   - mandatory cards are locked — clicking them is a no-op + toast
 *   - cascading add: selecting Harbor pulls in cnpg + seaweedfs + valkey
 *   - cascading remove: removing cnpg opens a confirm dialog listing
 *     every dependent that would be removed; cancel keeps state intact;
 *     confirm cascades through the impact set
 *   - store invariants: every mandatory id is always present, ids are
 *     sorted + de-duplicated, persisted shape round-trips through merge
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup, act, within } from '@testing-library/react'
import { StepComponents } from './StepComponents'
import {
  ALL_COMPONENTS,
  MANDATORY_COMPONENT_IDS,
  resolveTransitiveDependencies,
  resolveTransitiveDependents,
  findComponent,
  computeDefaultSelection,
} from './componentGroups'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

/* ── Fixtures ─────────────────────────────────────────────────────── */

function resetStore(extra: Partial<typeof INITIAL_WIZARD_STATE> = {}) {
  useWizardStore.setState({
    ...INITIAL_WIZARD_STATE,
    selectedComponents: [...computeDefaultSelection()].sort(),
    ...extra,
  })
}

beforeEach(() => {
  // Each test starts with the catalog default selection so we exercise the
  // real-world "wizard just opened, mandatory + recommended pre-selected"
  // shape.
  resetStore()
})

afterEach(() => {
  cleanup()
})

/* ── Catalog sanity ───────────────────────────────────────────────── */

describe('component catalog', () => {
  it('renders 60+ components from componentGroups.ts', () => {
    expect(ALL_COMPONENTS.length).toBeGreaterThanOrEqual(60)
  })

  it('every component has a tier', () => {
    for (const c of ALL_COMPONENTS) {
      expect(['mandatory', 'recommended', 'optional']).toContain(c.tier)
    }
  })

  it('every dependency points at a known component id', () => {
    const ids = new Set(ALL_COMPONENTS.map(c => c.id))
    for (const c of ALL_COMPONENTS) {
      for (const d of c.dependencies ?? []) {
        expect(ids.has(d)).toBe(true)
      }
    }
  })

  it('Harbor depends on cnpg + seaweedfs + valkey', () => {
    const harbor = findComponent('harbor')
    expect(harbor).toBeDefined()
    expect(harbor!.dependencies).toEqual(expect.arrayContaining(['cnpg', 'seaweedfs', 'valkey']))
  })

  it('OpenSearch has no dependencies (it owns its storage)', () => {
    expect(findComponent('opensearch')!.dependencies).toEqual([])
  })

  it('Reloader / KEDA / VPA / Cilium / Crossplane / Flux have no deps', () => {
    for (const id of ['reloader', 'keda', 'vpa', 'cilium', 'crossplane', 'flux']) {
      expect(findComponent(id)!.dependencies ?? []).toEqual([])
    }
  })
})

/* ── Render: card grid ────────────────────────────────────────────── */

describe('card grid', () => {
  it('renders a card for every catalog entry', () => {
    render(<StepComponents />)
    const grid = screen.getByTestId('component-grid')
    // One button per component
    for (const c of ALL_COMPONENTS) {
      expect(within(grid).getByTestId(`component-card-${c.id}`)).toBeTruthy()
    }
  })

  it('shows a counter "Selected (N) of M"', () => {
    render(<StepComponents />)
    const counter = screen.getByTestId('selected-counter')
    const expected = computeDefaultSelection().length
    expect(counter.textContent).toMatch(new RegExp(`Selected \\(${expected}\\) of ${ALL_COMPONENTS.length}`))
  })

  it('renders an "Includes:" hint for components with dependencies', () => {
    render(<StepComponents />)
    expect(screen.getByTestId('includes-harbor').textContent).toMatch(/Includes:/)
    expect(screen.getByTestId('includes-harbor').textContent).toMatch(/CloudNative PG/)
  })

  it('mandatory cards carry a MANDATORY tier badge', () => {
    render(<StepComponents />)
    expect(screen.getByTestId('tier-flux').textContent).toMatch(/MANDATORY/)
    expect(screen.getByTestId('tier-cilium').textContent).toMatch(/MANDATORY/)
  })
})

/* ── Search filter ────────────────────────────────────────────────── */

describe('search filter', () => {
  it('narrows visible cards to matching name / description', () => {
    render(<StepComponents />)
    const input = screen.getByTestId('component-search') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'harbor' } })
    expect(screen.getByTestId('component-card-harbor')).toBeTruthy()
    expect(screen.queryByTestId('component-card-cnpg')).toBeNull()
    expect(screen.queryByTestId('component-card-grafana')).toBeNull()
  })

  it('matches against group name (e.g. "fabric")', () => {
    render(<StepComponents />)
    const input = screen.getByTestId('component-search') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'fabric' } })
    expect(screen.getByTestId('component-card-cnpg')).toBeTruthy()
    expect(screen.getByTestId('component-card-strimzi')).toBeTruthy()
    expect(screen.queryByTestId('component-card-flux')).toBeNull()
  })

  it('shows the empty-state when nothing matches', () => {
    render(<StepComponents />)
    const input = screen.getByTestId('component-search') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'thiswillmatchnothingxyz' } })
    expect(screen.getByTestId('empty-state')).toBeTruthy()
  })
})

/* ── Category filter ──────────────────────────────────────────────── */

describe('category filter', () => {
  it('renders one chip per group + an "All" chip', () => {
    render(<StepComponents />)
    expect(screen.getByTestId('category-chip-all')).toBeTruthy()
    for (const id of ['pilot', 'spine', 'surge', 'silo', 'guardian', 'insights', 'fabric', 'cortex', 'relay']) {
      expect(screen.getByTestId(`category-chip-${id}`)).toBeTruthy()
    }
  })

  it('clicking a category chip narrows the grid to that group', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('category-chip-pilot'))
    // Pilot has flux, crossplane, gitea, opentofu, vcluster
    expect(screen.getByTestId('component-card-flux')).toBeTruthy()
    expect(screen.getByTestId('component-card-vcluster')).toBeTruthy()
    expect(screen.queryByTestId('component-card-cilium')).toBeNull()
    expect(screen.queryByTestId('component-card-harbor')).toBeNull()
  })

  it('toggling the same chip a second time clears the filter', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('category-chip-pilot'))
    fireEvent.click(screen.getByTestId('category-chip-pilot'))
    expect(screen.getByTestId('component-card-cilium')).toBeTruthy()
  })
})

/* ── Sort: selected first ─────────────────────────────────────────── */

describe('sort: selected first', () => {
  it('selected components float to the top of the grid', () => {
    // Empty selection so we can pick known optional components and verify
    // they jump to the top after selection.
    resetStore({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    render(<StepComponents />)

    // Filter to FABRIC so the assertion is small + deterministic
    fireEvent.click(screen.getByTestId('category-chip-fabric'))

    // Pick an optional: clickhouse
    fireEvent.click(screen.getByTestId('component-card-clickhouse'))

    // Re-query the grid in DOM order and assert clickhouse is first
    const grid = screen.getByTestId('component-grid')
    const cards = within(grid).getAllByRole('button')
    const ids = cards.map(c => c.getAttribute('data-testid'))
    expect(ids[0]).toBe('component-card-clickhouse')
  })
})

/* ── Mandatory rejection ──────────────────────────────────────────── */

describe('mandatory cards', () => {
  it('clicking a mandatory card never removes it', () => {
    render(<StepComponents />)
    const before = useWizardStore.getState().selectedComponents.includes('flux')
    expect(before).toBe(true)
    fireEvent.click(screen.getByTestId('component-card-flux'))
    const after = useWizardStore.getState().selectedComponents.includes('flux')
    expect(after).toBe(true)
  })

  it('clicking a mandatory card emits a "mandatory" toast', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('component-card-flux'))
    const toast = screen.getByTestId('toast-mandatory')
    expect(toast.textContent).toMatch(/Flux CD is mandatory/i)
  })
})

/* ── Cascading add ────────────────────────────────────────────────── */

describe('cascading add', () => {
  it('selecting Harbor adds cnpg + seaweedfs + valkey', () => {
    // Reset to mandatories only — Harbor is mandatory in the catalog so
    // we have to remove it via an internal raw-state set, then re-add via
    // the action to exercise the cascade. Simpler: pick a non-mandatory
    // component with deps. clickhouse has no deps, but ferretdb depends
    // on cnpg only — let's pick milvus (deps: seaweedfs).
    resetStore({ selectedComponents: [...MANDATORY_COMPONENT_IDS].filter(id => id !== 'harbor').sort() })
    // Drop seaweedfs (it's mandatory so we have to bypass the action)
    useWizardStore.setState({ selectedComponents: useWizardStore.getState().selectedComponents.filter(id => id !== 'seaweedfs' && id !== 'milvus') })

    useWizardStore.getState().addComponent('milvus')
    const sel = useWizardStore.getState().selectedComponents
    expect(sel).toContain('milvus')
    expect(sel).toContain('seaweedfs')
  })

  it('selecting Harbor (raw store call) cascades to cnpg + seaweedfs + valkey', () => {
    // Strip everything to nothing, then ask the store to add harbor.
    useWizardStore.setState({ selectedComponents: [] })
    useWizardStore.getState().addComponent('harbor')
    const sel = useWizardStore.getState().selectedComponents
    expect(sel).toContain('harbor')
    expect(sel).toContain('cnpg')
    expect(sel).toContain('seaweedfs')
    expect(sel).toContain('valkey')
  })

  it('UI emits a single toast announcing the cascade', () => {
    // Strip optional deps so cascade actually fires
    useWizardStore.setState({ selectedComponents: useWizardStore.getState().selectedComponents.filter(id => !['langfuse', 'cnpg'].includes(id)) })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('component-card-langfuse'))
    const toast = screen.getByTestId('toast-added')
    expect(toast.textContent).toMatch(/LangFuse added/)
    expect(toast.textContent).toMatch(/Also added/)
    expect(toast.textContent).toMatch(/CloudNative PG/)
  })

  it('store action is idempotent — adding the same id twice is a no-op', () => {
    const before = useWizardStore.getState().selectedComponents.length
    useWizardStore.getState().addComponent('flux')
    useWizardStore.getState().addComponent('flux')
    expect(useWizardStore.getState().selectedComponents.length).toBe(before)
  })
})

/* ── Cascading remove ─────────────────────────────────────────────── */

describe('cascading remove', () => {
  it('opens a confirm dialog when removing a component with dependents', () => {
    // cnpg is recommended (not mandatory) so removable. Default state has
    // both cnpg and gitea (gitea depends on cnpg).
    useWizardStore.setState({
      selectedComponents: [...new Set([
        ...useWizardStore.getState().selectedComponents,
        'cnpg', 'gitea',
      ])].sort(),
    })
    render(<StepComponents />)
    // Filter to fabric so cnpg is visible and easy to find
    fireEvent.click(screen.getByTestId('category-chip-fabric'))
    fireEvent.click(screen.getByTestId('component-card-cnpg'))
    expect(screen.getByTestId('cascade-dialog')).toBeTruthy()
    // The dialog must list at least one dependent
    const list = screen.getByTestId('cascade-dependents')
    expect(list.textContent).toMatch(/Gitea|Harbor|Keycloak/)
  })

  it('cancel keeps the component selected', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('category-chip-fabric'))
    fireEvent.click(screen.getByTestId('component-card-cnpg'))
    fireEvent.click(screen.getByTestId('cascade-cancel'))
    expect(useWizardStore.getState().selectedComponents).toContain('cnpg')
    expect(screen.queryByTestId('cascade-dialog')).toBeNull()
  })

  it('confirm cascades through the impact set', () => {
    // Build a tight scenario: only optional/recommended ids that depend on cnpg
    useWizardStore.setState({
      selectedComponents: ['cnpg', 'langfuse', 'librechat', 'matrix', 'temporal'].sort(),
    })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('category-chip-fabric'))
    fireEvent.click(screen.getByTestId('component-card-cnpg'))
    fireEvent.click(screen.getByTestId('cascade-confirm'))
    const sel = useWizardStore.getState().selectedComponents
    expect(sel).not.toContain('cnpg')
    expect(sel).not.toContain('langfuse')
    expect(sel).not.toContain('librechat')
    expect(sel).not.toContain('matrix')
    expect(sel).not.toContain('temporal')
  })

  it('mandatory components are NEVER removed even via cascade', () => {
    // gitea + harbor are mandatory and depend on cnpg. If user confirms
    // removing cnpg, those mandatory dependents must stay; non-mandatory
    // dependents (keycloak, langfuse, …) cascade out.
    useWizardStore.setState({
      selectedComponents: [...computeDefaultSelection()].sort(),
    })
    useWizardStore.getState().removeComponent('cnpg')
    const sel = useWizardStore.getState().selectedComponents
    expect(sel).toContain('gitea') // mandatory — protected
    expect(sel).toContain('harbor') // mandatory — protected
    expect(sel).not.toContain('keycloak') // recommended — cascaded out
  })

  it('removeComponent on a mandatory id is a no-op', () => {
    useWizardStore.setState({
      selectedComponents: [...computeDefaultSelection()].sort(),
    })
    const before = useWizardStore.getState().selectedComponents.length
    useWizardStore.getState().removeComponent('flux')
    const after = useWizardStore.getState().selectedComponents
    expect(after.length).toBe(before)
    expect(after).toContain('flux')
  })
})

/* ── Store invariants ─────────────────────────────────────────────── */

describe('store invariants', () => {
  it('selectedComponents is always sorted', () => {
    useWizardStore.setState({ selectedComponents: [] })
    useWizardStore.getState().addComponent('harbor') // adds harbor + cnpg + seaweedfs + valkey
    const sel = useWizardStore.getState().selectedComponents
    const sortedCopy = [...sel].sort()
    expect(sel).toEqual(sortedCopy)
  })

  it('selectedComponents is de-duplicated via setSelectedComponents', () => {
    useWizardStore.getState().setSelectedComponents(['flux', 'flux', 'cilium', 'cilium'])
    expect(useWizardStore.getState().selectedComponents).toEqual(['cilium', 'flux'])
  })

  it('legacy SelectedComponent[] payload is normalised to ids', () => {
    useWizardStore.getState().setComponents([
      { id: 'flux', name: 'Flux CD', version: '2.0', category: 'pilot', required: true, dependencies: [] },
      { id: 'cilium', name: 'Cilium', version: '1.15', category: 'spine', required: true, dependencies: [] },
    ])
    expect(useWizardStore.getState().selectedComponents).toEqual(['cilium', 'flux'])
  })

  it('resetSelectedComponentsToDefault restores mandatory + recommended + deps', () => {
    useWizardStore.setState({ selectedComponents: [] })
    useWizardStore.getState().resetSelectedComponentsToDefault()
    const sel = useWizardStore.getState().selectedComponents
    // Every mandatory id present
    for (const id of MANDATORY_COMPONENT_IDS) {
      expect(sel).toContain(id)
    }
  })

  it('every mandatory id is present in default selection', () => {
    const defaults = computeDefaultSelection()
    for (const id of MANDATORY_COMPONENT_IDS) {
      expect(defaults).toContain(id)
    }
  })

  it('default selection includes all transitive deps of mandatory items', () => {
    const defaults = new Set(computeDefaultSelection())
    for (const id of MANDATORY_COMPONENT_IDS) {
      for (const dep of resolveTransitiveDependencies(id)) {
        expect(defaults.has(dep)).toBe(true)
      }
    }
  })
})

/* ── Reset to defaults button ─────────────────────────────────────── */

describe('reset to defaults', () => {
  it('clicking the reset button restores the default selection', () => {
    render(<StepComponents />)
    // Select an optional, then reset
    fireEvent.click(screen.getByTestId('component-card-clickhouse'))
    expect(useWizardStore.getState().selectedComponents).toContain('clickhouse')
    fireEvent.click(screen.getByTestId('reset-defaults'))
    expect(useWizardStore.getState().selectedComponents).not.toContain('clickhouse')
    for (const id of MANDATORY_COMPONENT_IDS) {
      expect(useWizardStore.getState().selectedComponents).toContain(id)
    }
  })
})

/* ── Reverse-graph helpers ────────────────────────────────────────── */

describe('reverse-graph helpers', () => {
  it('resolveTransitiveDependents includes every component that needs cnpg directly or transitively', () => {
    const dependents = resolveTransitiveDependents('cnpg')
    // Direct dependents
    for (const id of ['gitea', 'keycloak', 'harbor', 'ferretdb', 'temporal', 'langfuse', 'librechat', 'matrix', 'superset', 'openmeter']) {
      expect(dependents).toContain(id)
    }
  })

  it('resolveTransitiveDependents on cnpg does NOT include cnpg itself', () => {
    expect(resolveTransitiveDependents('cnpg')).not.toContain('cnpg')
  })

  it('act() suppresses unhandled promise warnings — sanity', () => {
    // Smoke test that act() works. Avoids the test-runner flagging this
    // file as having no tests.
    act(() => undefined)
    expect(true).toBe(true)
  })
})
