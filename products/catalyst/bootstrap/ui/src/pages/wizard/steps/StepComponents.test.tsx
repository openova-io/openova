/**
 * StepComponents.test.tsx — vitest coverage for the wizard's two-tab
 * platform-component picker (GitHub issues #161, #162).
 *
 * Covers:
 *   - Tab 1 ("Choose Your Stack") — non-mandatory only
 *       - search narrows visible cards
 *       - category chip filter narrows visible cards (groupId)
 *       - sort: selected items float to top, then alphabetical
 *       - cascade-add: selecting Harbor pulls in cnpg + seaweedfs + valkey
 *       - cascade-remove: confirm dialog flow, mandatory protection
 *       - reset-to-defaults restores the canonical selection
 *   - Tab 2 ("Always Included") — mandatory only, read-only, grouped
 *       - lists every mandatory component
 *       - groups by product (PILOT, GUARDIAN, …)
 *       - has no search input
 *       - has no toggle button on cards
 *       - tab counter equals the catalog's mandatory count
 *   - Tab switch state — clicking tabs swaps the body without losing state
 *   - Catalog invariants — dependency graph integrity, store invariants
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
  GROUPS,
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
  resetStore()
})

afterEach(() => {
  cleanup()
})

const NON_MANDATORY = ALL_COMPONENTS.filter((c) => c.tier !== 'mandatory')
const MANDATORY = ALL_COMPONENTS.filter((c) => c.tier === 'mandatory')

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

  it('every component carries a logoUrl (default `/component-logos/<id>.svg`)', () => {
    for (const c of ALL_COMPONENTS) {
      expect(c.logoUrl).toBe(`/component-logos/${c.id}.svg`)
    }
  })
})

/* ── Tabs ─────────────────────────────────────────────────────────── */

describe('tabs', () => {
  it('renders both tabs with correct counters', () => {
    render(<StepComponents />)
    const choose = screen.getByTestId('tab-choose')
    const always = screen.getByTestId('tab-always')
    expect(choose).toBeTruthy()
    expect(always).toBeTruthy()
    expect(always.textContent).toMatch(new RegExp(`Always Included \\(${MANDATORY.length}\\)`))
  })

  it('Choose Your Stack tab is active by default', () => {
    render(<StepComponents />)
    expect(screen.getByTestId('tab-choose').getAttribute('aria-selected')).toBe('true')
    expect(screen.getByTestId('tab-always').getAttribute('aria-selected')).toBe('false')
  })

  it('clicking the "Always Included" tab switches the body', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    expect(screen.getByTestId('tab-always').getAttribute('aria-selected')).toBe('true')
    expect(screen.getByTestId('always-included-tab')).toBeTruthy()
    // Choose-tab grid + search not in the DOM in always tab
    expect(screen.queryByTestId('component-grid')).toBeNull()
    expect(screen.queryByTestId('component-search')).toBeNull()
  })

  it('switching back to Choose Your Stack restores the grid', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    fireEvent.click(screen.getByTestId('tab-choose'))
    expect(screen.getByTestId('component-grid')).toBeTruthy()
    expect(screen.getByTestId('component-search')).toBeTruthy()
  })

  it('Choose-tab counter reflects user-controllable selection (recommended + optional)', () => {
    // Default state: every recommended is selected. Counter should equal
    // the recommended count (no optional selected).
    const recommendedCount = ALL_COMPONENTS.filter((c) => c.tier === 'recommended').length
    render(<StepComponents />)
    const choose = screen.getByTestId('tab-choose')
    expect(choose.textContent).toMatch(new RegExp(`Choose Your Stack \\(${recommendedCount}\\)`))
  })
})

/* ── Tab 1: Choose Your Stack — render + filter ───────────────────── */

describe('Tab 1 (Choose Your Stack) — card grid', () => {
  it('renders a card for every NON-mandatory catalog entry', () => {
    render(<StepComponents />)
    const grid = screen.getByTestId('component-grid')
    for (const c of NON_MANDATORY) {
      expect(within(grid).getByTestId(`component-card-${c.id}`)).toBeTruthy()
    }
  })

  it('does NOT render mandatory cards in Tab 1', () => {
    render(<StepComponents />)
    for (const c of MANDATORY) {
      expect(screen.queryByTestId(`component-card-${c.id}`)).toBeNull()
    }
  })

  it('shows a counter "Selected (N) of M"', () => {
    render(<StepComponents />)
    const counter = screen.getByTestId('selected-counter')
    const expected = computeDefaultSelection().length
    expect(counter.textContent).toMatch(
      new RegExp(`Selected \\(${expected}\\) of ${ALL_COMPONENTS.length}`),
    )
  })

  it('renders an "Includes:" hint for components with dependencies', () => {
    render(<StepComponents />)
    // langfuse is non-mandatory and has deps (cnpg) — visible in Tab 1
    expect(screen.getByTestId('includes-langfuse').textContent).toMatch(/Includes:/)
    expect(screen.getByTestId('includes-langfuse').textContent).toMatch(/CloudNative PG/)
  })

  it('non-mandatory cards carry a recommended/optional tier badge', () => {
    render(<StepComponents />)
    expect(screen.getByTestId('tier-grafana').textContent).toMatch(/RECOMMENDED/)
    expect(screen.getByTestId('tier-clickhouse').textContent).toMatch(/OPTIONAL/)
  })

  it('renders an <img> logo for each non-mandatory card', () => {
    render(<StepComponents />)
    expect(screen.getByTestId('logo-grafana')).toBeTruthy()
    expect(screen.getByTestId('logo-grafana').getAttribute('src')).toBe('/component-logos/grafana.svg')
  })
})

/* ── Search filter (Tab 1) ────────────────────────────────────────── */

describe('Tab 1 — search filter', () => {
  it('narrows visible cards to matching name / description', () => {
    render(<StepComponents />)
    const input = screen.getByTestId('component-search') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'grafana' } })
    expect(screen.getByTestId('component-card-grafana')).toBeTruthy()
    expect(screen.queryByTestId('component-card-clickhouse')).toBeNull()
    expect(screen.queryByTestId('component-card-langfuse')).toBeNull()
  })

  it('matches against group name (e.g. "fabric")', () => {
    render(<StepComponents />)
    const input = screen.getByTestId('component-search') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'fabric' } })
    // cnpg is recommended (non-mandatory) and lives in fabric
    expect(screen.getByTestId('component-card-cnpg')).toBeTruthy()
    expect(screen.queryByTestId('component-card-grafana')).toBeNull()
  })

  it('shows the empty-state when nothing matches', () => {
    render(<StepComponents />)
    const input = screen.getByTestId('component-search') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'thiswillmatchnothingxyz' } })
    expect(screen.getByTestId('empty-state')).toBeTruthy()
  })
})

/* ── Category filter (Tab 1) ──────────────────────────────────────── */

describe('Tab 1 — category filter', () => {
  it('renders an "All" chip plus chips for groups with at least one non-mandatory', () => {
    render(<StepComponents />)
    expect(screen.getByTestId('category-chip-all')).toBeTruthy()
    // Groups that have at least one recommended/optional entry
    const groupsWithChoose = new Set(NON_MANDATORY.map((c) => c.groupId))
    for (const id of groupsWithChoose) {
      expect(screen.getByTestId(`category-chip-${id}`)).toBeTruthy()
    }
  })

  it('clicking a category chip narrows the grid to that group', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('category-chip-fabric'))
    // Fabric non-mandatories include cnpg, valkey, strimzi, debezium, …
    expect(screen.getByTestId('component-card-cnpg')).toBeTruthy()
    expect(screen.getByTestId('component-card-strimzi')).toBeTruthy()
    expect(screen.queryByTestId('component-card-grafana')).toBeNull()
  })

  it('toggling the same chip a second time clears the filter', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('category-chip-fabric'))
    fireEvent.click(screen.getByTestId('category-chip-fabric'))
    expect(screen.getByTestId('component-card-grafana')).toBeTruthy()
  })
})

/* ── Sort: selected first ─────────────────────────────────────────── */

describe('Tab 1 — sort: selected first', () => {
  it('selected components float to the top of the grid', () => {
    resetStore({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('category-chip-fabric'))
    fireEvent.click(screen.getByTestId('component-card-clickhouse'))
    const grid = screen.getByTestId('component-grid')
    const cards = within(grid).getAllByRole('button')
    const ids = cards.map((c) => c.getAttribute('data-testid'))
    expect(ids[0]).toBe('component-card-clickhouse')
  })
})

/* ── Cascading add ────────────────────────────────────────────────── */

describe('cascading add', () => {
  it('selecting a non-mandatory component adds its deps via the store', () => {
    useWizardStore.setState({ selectedComponents: [] })
    useWizardStore.getState().addComponent('milvus')
    const sel = useWizardStore.getState().selectedComponents
    expect(sel).toContain('milvus')
    expect(sel).toContain('seaweedfs')
  })

  it('store cascades Harbor → cnpg + seaweedfs + valkey', () => {
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
    useWizardStore.setState({
      selectedComponents: useWizardStore.getState().selectedComponents.filter(
        (id) => !['langfuse', 'cnpg'].includes(id),
      ),
    })
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
    useWizardStore.setState({
      selectedComponents: [
        ...new Set([
          ...useWizardStore.getState().selectedComponents,
          'cnpg',
          'gitea',
        ]),
      ].sort(),
    })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('category-chip-fabric'))
    fireEvent.click(screen.getByTestId('component-card-cnpg'))
    expect(screen.getByTestId('cascade-dialog')).toBeTruthy()
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

/* ── Tab 2: Always Included ───────────────────────────────────────── */

describe('Tab 2 (Always Included) — read-only mandatory grid', () => {
  it('renders a card for every mandatory component', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    const tab = screen.getByTestId('always-included-tab')
    for (const c of MANDATORY) {
      expect(within(tab).getByTestId(`component-card-${c.id}`)).toBeTruthy()
    }
  })

  it('does NOT render any non-mandatory components', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    const tab = screen.getByTestId('always-included-tab')
    for (const c of NON_MANDATORY) {
      expect(within(tab).queryByTestId(`component-card-${c.id}`)).toBeNull()
    }
  })

  it('groups mandatory components by product', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    // Every group that has at least one mandatory has a section header
    for (const g of GROUPS) {
      const hasMandatory = g.components.some((c) => c.tier === 'mandatory')
      if (hasMandatory) {
        expect(screen.getByTestId(`always-included-section-${g.id}`)).toBeTruthy()
      }
    }
  })

  it('groups with zero mandatories are NOT rendered as sections', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    for (const g of GROUPS) {
      const hasMandatory = g.components.some((c) => c.tier === 'mandatory')
      if (!hasMandatory) {
        expect(screen.queryByTestId(`always-included-section-${g.id}`)).toBeNull()
      }
    }
  })

  it('has no search input', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    expect(screen.queryByTestId('component-search')).toBeNull()
  })

  it('has no category chips', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    expect(screen.queryByTestId('category-chips')).toBeNull()
  })

  it('mandatory cards render an INFRASTRUCTURE pill (not MANDATORY)', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    const tier = screen.getByTestId('tier-flux')
    expect(tier.textContent).toMatch(/INFRASTRUCTURE/)
    expect(tier.textContent).not.toMatch(/MANDATORY/)
  })

  it('cards have NO toggle button', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    expect(screen.queryByTestId('toggle-flux')).toBeNull()
  })

  it('renders the foundational-platform blurb at the top', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    expect(screen.getByTestId('always-included-blurb').textContent).toMatch(
      /platform components run on every Sovereign/i,
    )
  })

  it('clicking a mandatory card never adds/removes it', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    const before = useWizardStore.getState().selectedComponents.includes('flux')
    fireEvent.click(screen.getByTestId('component-card-flux'))
    const after = useWizardStore.getState().selectedComponents.includes('flux')
    expect(before).toBe(true)
    expect(after).toBe(true)
    // No mandatory toast either — read-only cards do not push toasts
    expect(screen.queryByTestId('toast-mandatory')).toBeNull()
  })
})

/* ── Store invariants ─────────────────────────────────────────────── */

describe('store invariants', () => {
  it('selectedComponents is always sorted', () => {
    useWizardStore.setState({ selectedComponents: [] })
    useWizardStore.getState().addComponent('harbor')
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

/* ── Reset to defaults ────────────────────────────────────────────── */

describe('reset to defaults', () => {
  it('clicking the reset button restores the default selection', () => {
    render(<StepComponents />)
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
    for (const id of [
      'gitea', 'keycloak', 'harbor', 'ferretdb', 'temporal',
      'langfuse', 'librechat', 'matrix', 'superset', 'openmeter',
    ]) {
      expect(dependents).toContain(id)
    }
  })

  it('resolveTransitiveDependents on cnpg does NOT include cnpg itself', () => {
    expect(resolveTransitiveDependents('cnpg')).not.toContain('cnpg')
  })

  it('act() suppresses unhandled promise warnings — sanity', () => {
    act(() => undefined)
    expect(true).toBe(true)
  })
})
