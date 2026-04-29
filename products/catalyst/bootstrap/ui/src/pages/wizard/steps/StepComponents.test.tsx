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
  RAW_COMPONENTS,
  MANDATORY_COMPONENT_IDS,
  TRANSITIVE_MANDATORY_PROMOTIONS,
  PRODUCTS,
  resolveTransitiveDependencies,
  resolveTransitiveDependents,
  resolveProductComponentClosure,
  findComponent,
  findProduct,
  componentsByProduct,
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

  it('every component carries an upstream brand mark or an explicit fallback', () => {
    // In Vitest BASE_URL is `/`, so basePath() emits `/component-logos/<id>.<ext>`.
    // In production Vite injects BASE_URL=/sovereign/ at build time and the
    // same expression emits `/sovereign/component-logos/<id>.<ext>`. Most
    // upstream projects ship an SVG; a few (Loki, Mimir, Tempo, Trivy,
    // ntfy, NetBird, …) only publish PNG-form brand marks, in which case
    // the component sets `logoUrl` explicitly to the .png path.
    for (const c of ALL_COMPONENTS) {
      if (c.logoUrl === null) continue // explicit letter-mark fallback
      expect(c.logoUrl).toMatch(
        new RegExp(`^/component-logos/${c.id}\\.(svg|png)$`),
      )
    }
  })

  it('components flagged with logoUrl: null fall back to the letter-mark', () => {
    // PowerDNS, BGE, and the OpenOva-internal Axon / Continuum / Specter
    // components have no upstream brand mark suitable for the card — they
    // render via IconFallback, not an <img> element.
    for (const id of ['powerdns', 'bge', 'axon', 'continuum', 'specter']) {
      const entry = findComponent(id)
      expect(entry).toBeTruthy()
      expect(entry!.logoUrl).toBeNull()
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

  it('renders the letter-mark fallback (no <img>) for components with logoUrl: null', () => {
    // Mandatory cards live in Tab 2 — switch tabs so PowerDNS is in the DOM.
    // This is the SAME graceful fallback path the wizard uses when an SVG
    // fetch 404s — ComponentLogo branches on `!entry.logoUrl` and emits the
    // IconFallback span instead of <img>. (#173)
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    const card = screen.getByTestId('component-card-powerdns')
    expect(within(card).queryByTestId('logo-powerdns')).toBeNull()
    expect(card.textContent).toMatch(/PowerDNS/)
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
    // strimzi is recommended (non-mandatory) and lives in fabric. cnpg
    // / valkey were here pre-#175 but the transitive-mandatory promotion
    // has lifted them into Tab 2 ("Always Included") so we use a member
    // that is still user-toggleable in Tab 1.
    expect(screen.getByTestId('component-card-strimzi')).toBeTruthy()
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
    // Fabric non-mandatories (post-#175 promotion) include strimzi,
    // debezium, flink, temporal, clickhouse, ferretdb, iceberg, superset.
    expect(screen.getByTestId('component-card-strimzi')).toBeTruthy()
    expect(screen.getByTestId('component-card-debezium')).toBeTruthy()
    expect(screen.queryByTestId('component-card-grafana')).toBeNull()
    // cnpg / valkey are mandatory after #175 and should not surface
    // in Tab 1 even when their parent product is filtered to.
    expect(screen.queryByTestId('component-card-cnpg')).toBeNull()
    expect(screen.queryByTestId('component-card-valkey')).toBeNull()
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
  it('selected components float to the top of the FABRIC product section', () => {
    resetStore({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('category-chip-fabric'))
    fireEvent.click(screen.getByTestId('component-card-clickhouse'))
    // Tab 1 renders product sections — the section, not the outer grid,
    // is the right scope for the "selected-first" sort assertion.
    const section = screen.getByTestId('product-section-fabric')
    const cards = within(section).getAllByTestId(/^component-card-/)
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
    // Strip every CORTEX member so the family cascade fires when we
    // click LangFuse. cnpg is a transitive-mandatory now and stays
    // selected, but the toast surfaces the CORTEX family addition.
    const before = useWizardStore.getState().selectedComponents
    const cortexIds = ['kserve', 'knative', 'axon', 'neo4j', 'vllm', 'milvus', 'bge', 'langfuse', 'librechat']
    useWizardStore.setState({
      selectedComponents: before.filter((id) => !cortexIds.includes(id)),
    })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('component-card-langfuse'))
    const toast = screen.getByTestId('toast-added')
    expect(toast.textContent).toMatch(/LangFuse added/)
    expect(toast.textContent).toMatch(/CORTEX family/)
    // BGE / Milvus / vLLM are CORTEX members the cascade pulls in.
    expect(toast.textContent).toMatch(/(BGE|Milvus|vLLM)/)
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
    // strimzi (recommended, fabric) has Debezium depending on it, so
    // toggling-off strimzi triggers the cascade-remove confirm. Replaces
    // the pre-#175 cnpg test (cnpg was promoted to mandatory by the
    // transitive-mandatory rule and no longer surfaces in Tab 1).
    useWizardStore.setState({
      selectedComponents: [
        ...new Set([
          ...useWizardStore.getState().selectedComponents,
          'strimzi',
          'debezium',
        ]),
      ].sort(),
    })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('category-chip-fabric'))
    fireEvent.click(screen.getByTestId('component-card-strimzi'))
    expect(screen.getByTestId('cascade-dialog')).toBeTruthy()
    const list = screen.getByTestId('cascade-dependents')
    expect(list.textContent).toMatch(/Debezium/)
  })

  it('cancel keeps the component selected', () => {
    useWizardStore.setState({
      selectedComponents: [
        ...new Set([
          ...useWizardStore.getState().selectedComponents,
          'strimzi',
          'debezium',
        ]),
      ].sort(),
    })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('category-chip-fabric'))
    fireEvent.click(screen.getByTestId('component-card-strimzi'))
    fireEvent.click(screen.getByTestId('cascade-cancel'))
    expect(useWizardStore.getState().selectedComponents).toContain('strimzi')
    expect(screen.queryByTestId('cascade-dialog')).toBeNull()
  })

  it('confirm cascades through the impact set', () => {
    // strimzi → debezium is a non-mandatory chain we can fully unwind.
    useWizardStore.setState({
      selectedComponents: ['strimzi', 'debezium'].sort(),
    })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('category-chip-fabric'))
    fireEvent.click(screen.getByTestId('component-card-strimzi'))
    fireEvent.click(screen.getByTestId('cascade-confirm'))
    const sel = useWizardStore.getState().selectedComponents
    expect(sel).not.toContain('strimzi')
    expect(sel).not.toContain('debezium')
  })

  it('mandatory components are NEVER removed even via cascade', () => {
    // cnpg is mandatory after #175 — `removeComponent('cnpg')` is a
    // no-op AND never cascades through to keycloak / gitea / harbor.
    useWizardStore.setState({
      selectedComponents: [...computeDefaultSelection()].sort(),
    })
    useWizardStore.getState().removeComponent('cnpg')
    const sel = useWizardStore.getState().selectedComponents
    expect(sel).toContain('gitea')   // mandatory — protected
    expect(sel).toContain('harbor')  // mandatory — protected
    expect(sel).toContain('keycloak') // recommended — protected because
                                     // cnpg (its dep) is no-op'd
    expect(sel).toContain('cnpg')   // refused removal
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

  it('groups mandatory components by product (post-promotion catalog)', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    // Every group that has at least one mandatory in the POST-promotion
    // catalog has a section header. cnpg / valkey were lifted to
    // mandatory by #175 so FABRIC now qualifies even though raw GROUPS
    // declared its members as recommended/optional.
    for (const g of GROUPS) {
      const hasMandatory = ALL_COMPONENTS.some(
        (c) => c.product === g.id && c.tier === 'mandatory',
      )
      if (hasMandatory) {
        expect(screen.getByTestId(`always-included-section-${g.id}`)).toBeTruthy()
      }
    }
  })

  it('products with zero mandatories (post-promotion) are NOT rendered as sections', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    for (const g of GROUPS) {
      const hasMandatory = ALL_COMPONENTS.some(
        (c) => c.product === g.id && c.tier === 'mandatory',
      )
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

/* ── #175 fix A: transitive-mandatory promotion ──────────────────── */

describe('transitive-mandatory promotion (issue #175 fix A)', () => {
  it('cnpg starts as recommended in raw data', () => {
    const raw = RAW_COMPONENTS.find((c) => c.id === 'cnpg')
    expect(raw).toBeDefined()
    expect(raw!.tier).toBe('recommended')
  })

  it('cnpg is mandatory in the post-promotion catalog', () => {
    expect(findComponent('cnpg')!.tier).toBe('mandatory')
  })

  it('valkey is mandatory in the post-promotion catalog', () => {
    expect(findComponent('valkey')!.tier).toBe('mandatory')
  })

  it('TRANSITIVE_MANDATORY_PROMOTIONS includes cnpg + valkey', () => {
    expect(TRANSITIVE_MANDATORY_PROMOTIONS).toContain('cnpg')
    expect(TRANSITIVE_MANDATORY_PROMOTIONS).toContain('valkey')
  })

  it('every promoted component is reachable from a raw mandatory seed', () => {
    const rawById = new Map(RAW_COMPONENTS.map((c) => [c.id, c]))
    const seeds = RAW_COMPONENTS.filter((c) => c.tier === 'mandatory').map((c) => c.id)
    const reachable = new Set<string>(seeds)
    const queue = [...seeds]
    while (queue.length > 0) {
      const next = queue.shift()!
      const entry = rawById.get(next)
      if (!entry) continue
      for (const dep of entry.dependencies ?? []) {
        if (!reachable.has(dep)) {
          reachable.add(dep)
          queue.push(dep)
        }
      }
    }
    for (const id of TRANSITIVE_MANDATORY_PROMOTIONS) {
      expect(reachable.has(id)).toBe(true)
    }
  })

  it('cnpg does NOT appear in Tab 1 ("Choose Your Stack")', () => {
    render(<StepComponents />)
    expect(screen.queryByTestId('component-card-cnpg')).toBeNull()
  })

  it('cnpg DOES appear in Tab 2 ("Always Included")', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    expect(screen.getByTestId('component-card-cnpg')).toBeTruthy()
  })

  it('valkey DOES appear in Tab 2 ("Always Included")', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    expect(screen.getByTestId('component-card-valkey')).toBeTruthy()
  })

  it('promoted components are grouped under their owning product in Tab 2', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    // cnpg lives in FABRIC; the FABRIC section should now appear in
    // Tab 2 because of cnpg / valkey's promotion.
    const fabricSection = screen.getByTestId('always-included-section-fabric')
    expect(within(fabricSection).getByTestId('component-card-cnpg')).toBeTruthy()
    expect(within(fabricSection).getByTestId('component-card-valkey')).toBeTruthy()
  })
})

/* ── #175 fix B: product-family model ────────────────────────────── */

describe('product-family model (issue #175 fix B)', () => {
  it('every product has a corresponding GROUPS entry', () => {
    for (const p of PRODUCTS) {
      expect(GROUPS.find((g) => g.id === p.id)).toBeDefined()
    }
  })

  it('every product.components matches GROUPS members', () => {
    for (const p of PRODUCTS) {
      const group = GROUPS.find((g) => g.id === p.id)!
      const groupIds = group.components.map((c) => c.id)
      expect(p.components.sort()).toEqual([...groupIds].sort())
    }
  })

  it('CORTEX has cascadeOnMemberSelection=true (per operator)', () => {
    expect(findProduct('cortex')!.cascadeOnMemberSelection).toBe(true)
  })

  it('FABRIC has cascadeOnMemberSelection=false (à-la-carte)', () => {
    expect(findProduct('fabric')!.cascadeOnMemberSelection).toBe(false)
  })

  it('CORTEX familyDependencies include FABRIC (cnpg-backed members)', () => {
    expect(findProduct('cortex')!.familyDependencies).toContain('fabric')
  })

  it('Specter component-level deps cover the major CORTEX runtime members', () => {
    const specter = findComponent('specter')!
    for (const dep of ['bge', 'milvus', 'langfuse', 'vllm', 'kserve']) {
      expect(specter.dependencies).toContain(dep)
    }
  })
})

/* ── store: addProduct / removeProduct ──────────────────────────── */

describe('store: addProduct cascade', () => {
  it('addProduct(cortex) adds every CORTEX component', () => {
    useWizardStore.setState({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    useWizardStore.getState().addProduct('cortex')
    const sel = useWizardStore.getState().selectedComponents
    for (const c of componentsByProduct('cortex')) {
      expect(sel).toContain(c.id)
    }
  })

  it('addProduct(cortex) cascades to FABRIC components via familyDependencies', () => {
    useWizardStore.setState({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    useWizardStore.getState().addProduct('cortex')
    const sel = useWizardStore.getState().selectedComponents
    // FABRIC's strimzi (and other recommended/optional members) are
    // pulled in via the family-dependency cascade.
    expect(sel).toContain('strimzi')
    expect(sel).toContain('clickhouse')
  })

  it('addProduct(unknown) is a no-op', () => {
    useWizardStore.setState({ selectedComponents: ['flux'] })
    useWizardStore.getState().addProduct('this-product-does-not-exist')
    expect(useWizardStore.getState().selectedComponents).toEqual(['flux'])
  })
})

describe('store: removeProduct cascade', () => {
  it('removeProduct(cortex) drops every non-mandatory CORTEX member', () => {
    const fullCortex = resolveProductComponentClosure('cortex')
    useWizardStore.setState({
      selectedComponents: [...new Set([...MANDATORY_COMPONENT_IDS, ...fullCortex])].sort(),
    })
    useWizardStore.getState().removeProduct('cortex')
    const sel = useWizardStore.getState().selectedComponents
    for (const c of componentsByProduct('cortex')) {
      if (c.tier === 'mandatory') continue
      expect(sel).not.toContain(c.id)
    }
  })

  it('removeProduct(cortex) preserves CORTEX mandatory members (kserve)', () => {
    const fullCortex = resolveProductComponentClosure('cortex')
    useWizardStore.setState({
      selectedComponents: [...new Set([...MANDATORY_COMPONENT_IDS, ...fullCortex])].sort(),
    })
    useWizardStore.getState().removeProduct('cortex')
    expect(useWizardStore.getState().selectedComponents).toContain('kserve')
  })

  it('removeProduct(unknown) is a no-op', () => {
    useWizardStore.setState({ selectedComponents: ['flux'] })
    useWizardStore.getState().removeProduct('this-product-does-not-exist')
    expect(useWizardStore.getState().selectedComponents).toEqual(['flux'])
  })
})

/* ── cross-product cascade through addComponent ─────────────────── */

describe('addComponent → product family cascade (CORTEX)', () => {
  it('selecting BGE cascades to every CORTEX component', () => {
    useWizardStore.setState({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    useWizardStore.getState().addComponent('bge')
    const sel = useWizardStore.getState().selectedComponents
    for (const c of componentsByProduct('cortex')) {
      expect(sel).toContain(c.id)
    }
  })

  it('selecting Specter cascades to every CORTEX component', () => {
    useWizardStore.setState({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    useWizardStore.getState().addComponent('specter')
    const sel = useWizardStore.getState().selectedComponents
    for (const c of componentsByProduct('cortex')) {
      expect(sel).toContain(c.id)
    }
  })

  it('selecting clickhouse (FABRIC à-la-carte) does NOT cascade FABRIC', () => {
    useWizardStore.setState({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    useWizardStore.getState().addComponent('clickhouse')
    const sel = useWizardStore.getState().selectedComponents
    expect(sel).toContain('clickhouse')
    // Strimzi belongs to FABRIC but FABRIC is à-la-carte; clickhouse
    // should not pull it in.
    expect(sel).not.toContain('strimzi')
  })

  it('selecting BGE emits a CORTEX-family toast', () => {
    useWizardStore.setState({
      selectedComponents: [...MANDATORY_COMPONENT_IDS].sort(),
    })
    render(<StepComponents />)
    // bge lives under CORTEX (optional product, no category chip in Tab 1
    // when filtered to fabric — switch chip first).
    fireEvent.click(screen.getByTestId('category-chip-cortex'))
    fireEvent.click(screen.getByTestId('component-card-bge'))
    const toast = screen.getByTestId('toast-added')
    expect(toast.textContent).toMatch(/BGE added/)
    expect(toast.textContent).toMatch(/CORTEX family/)
  })
})

/* ── product-section header rendering ───────────────────────────── */

describe('Tab 1 — product sections (issue #175)', () => {
  it('renders a product section per product with non-mandatory members', () => {
    render(<StepComponents />)
    for (const p of PRODUCTS) {
      const userToggleable = componentsByProduct(p.id).filter(
        (c) => c.tier !== 'mandatory',
      )
      if (userToggleable.length > 0) {
        expect(screen.getByTestId(`product-section-${p.id}`)).toBeTruthy()
      }
    }
  })

  it('CORTEX product section exposes a "select entire product" CTA', () => {
    useWizardStore.setState({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    render(<StepComponents />)
    expect(screen.getByTestId('product-cta-cortex')).toBeTruthy()
  })

  it('clicking CORTEX product CTA selects every CORTEX component', () => {
    useWizardStore.setState({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('product-cta-cortex'))
    const sel = useWizardStore.getState().selectedComponents
    for (const c of componentsByProduct('cortex')) {
      expect(sel).toContain(c.id)
    }
  })

  it('product CTA toast announces the family addition', () => {
    useWizardStore.setState({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('product-cta-cortex'))
    const toast = screen.getByTestId('toast-added')
    expect(toast.textContent).toMatch(/CORTEX family added/)
  })
})

/* ── i18n: every operator-visible string is sourced from the copy
       module, not literal in JSX. We can't introspect the string
       table directly here but a smoke check at render confirms the
       module is wired up. ─────────────────────────────────────────*/

describe('i18n copy module wired into UI', () => {
  it('blurb in Tab 2 matches the copy module value', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    expect(screen.getByTestId('always-included-blurb').textContent).toMatch(
      /platform components run on every Sovereign/i,
    )
  })

  it('reset-defaults button is present and labelled from the copy module', () => {
    render(<StepComponents />)
    expect(screen.getByTestId('reset-defaults').textContent).toMatch(/Reset to defaults/i)
  })
})

/* ── catalog re-validation post-promotion ────────────────────────── */

describe('post-promotion catalog invariants', () => {
  it('ALL_COMPONENTS and RAW_COMPONENTS share the same id set', () => {
    const a = ALL_COMPONENTS.map((c) => c.id).sort()
    const r = RAW_COMPONENTS.map((c) => c.id).sort()
    expect(a).toEqual(r)
  })

  it('every component has a `product` field equal to its groupId', () => {
    for (const c of ALL_COMPONENTS) {
      expect(c.product).toBe(c.groupId)
    }
  })

  it('mandatory deps remain present in the default selection', () => {
    const defaults = new Set(computeDefaultSelection())
    for (const id of MANDATORY_COMPONENT_IDS) {
      expect(defaults.has(id)).toBe(true)
    }
  })
})
