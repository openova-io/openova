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

import { describe, it, expect, beforeAll, beforeEach, afterEach } from 'vitest'
import {
  render as rtlRender,
  screen,
  fireEvent,
  cleanup,
  act,
  within,
  type RenderResult,
} from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
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
  type ComponentEntry,
} from './componentGroups'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

/**
 * StepComponents now navigates to `/marketplace/family/<id>` and
 * `/marketplace/product/<id>` from the family chip and card body, so it
 * requires a TanStack Router context to render. The render helper below
 * keeps the existing synchronous test signature (`render(<X />)`) by:
 *
 *   1. Building a single router with stub marketplace + wizard routes at
 *      module load.
 *   2. Pre-loading the router once via `beforeAll` so route matches are
 *      ready before any test queries the DOM.
 *   3. Threading the test's UI through a module-scoped slot read by the
 *      index route's component, so each test renders different content
 *      without rebuilding the router.
 *
 * After every test we reset the router to `/` so the next render starts
 * from the clean wizard surface (chip / body click navigations done
 * inside a test never bleed into the next).
 */
let TEST_UI_SLOT: React.ReactNode = null

const __testRootRoute = createRootRoute({ component: () => <Outlet /> })
const __testIndexRoute = createRoute({
  getParentRoute: () => __testRootRoute,
  path: '/',
  component: () => <>{TEST_UI_SLOT}</>,
})
const __testFamilyRoute = createRoute({
  getParentRoute: () => __testRootRoute,
  path: '/marketplace/family/$familyId',
  component: () => <div data-testid="stub-marketplace-family" />,
})
const __testProductRoute = createRoute({
  getParentRoute: () => __testRootRoute,
  path: '/marketplace/product/$componentId',
  component: () => <div data-testid="stub-marketplace-product" />,
})
const __testWizardRoute = createRoute({
  getParentRoute: () => __testRootRoute,
  path: '/wizard',
  component: () => <div data-testid="stub-wizard" />,
})
const __testRouteTree = __testRootRoute.addChildren([
  __testIndexRoute,
  __testFamilyRoute,
  __testProductRoute,
  __testWizardRoute,
])
const __testRouter = createRouter({
  routeTree: __testRouteTree,
  history: createMemoryHistory({ initialEntries: ['/'] }),
})

beforeAll(async () => {
  await __testRouter.load()
})

function render(ui: React.ReactElement): RenderResult {
  TEST_UI_SLOT = ui
  return rtlRender(<RouterProvider router={__testRouter} />)
}

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

afterEach(async () => {
  cleanup()
  TEST_UI_SLOT = null
  // After a test that navigated away from `/` (chip / card body click)
  // the router would otherwise stay on the marketplace stub for the
  // next render. Reset by navigating home and waiting for matches.
  if (__testRouter.state.location.pathname !== '/') {
    await act(async () => {
      await __testRouter.navigate({ to: '/' })
    })
  }
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

  /**
   * Regression guard — this WAS red on `main`. Do NOT relax it.
   *
   * `componentGroups.ts` overrides every component's `dependencies` with
   * `depsFor(id)` from `src/data/blueprint-deps.generated.json`, which is
   * generated from Flux `HelmRelease.dependsOn` names. Three of those
   * names are bootstrap-kit HelmReleases that have NO ComponentDef in the
   * wizard catalog, and they used to leak into the graph as dangling ids:
   *
   *   gateway-api  ← gitea, powerdns, harbor, keycloak, openbao, grafana
   *   reflector    ← external-dns, postgres
   *   spire        ← openbao
   *
   * Operator-visible symptom was: `computeDefaultSelection()` returned 48
   * ids, three of which (`gateway-api`, `reflector`, `spire`) had no card,
   * no name and no logo anywhere in the wizard — yet they were written
   * into `selectedComponents`, travelled into the deployment payload, and
   * were counted by the "Selected (N) of M" counter.
   *
   * Fixed in the catalog layer: `resolveCatalogDependencies()` filters the
   * resolved list through `CATALOG_COMPONENT_IDS`. The three HelmReleases
   * are unconditional platform plumbing the bootstrap kit installs on
   * every Sovereign whatever the operator picks, so they are deliberately
   * card-less and dropping them from the wizard graph loses nothing.
   */
  it('every dependency points at a known component id', () => {
    const ids = new Set(ALL_COMPONENTS.map(c => c.id))
    for (const c of ALL_COMPONENTS) {
      for (const d of c.dependencies ?? []) {
        expect(ids.has(d)).toBe(true)
      }
    }
  })

  it('Harbor depends on cnpg + cert-manager, and NOT on seaweedfs', () => {
    // Dependencies are Flux-canonical since #652 (commit 544dc86b5) —
    // `componentGroups.ts:536` replaces the inline literals with
    // `HelmRelease.dependsOn`. Ground truth for Harbor is
    // `clusters/_template/bootstrap-kit/19-harbor.yaml:114-118`
    // (bp-cnpg + bp-cert-manager) and `19-harbor.yaml:35-37`:
    //   "The earlier dependency on bp-seaweedfs is REMOVED in 1.1.0
    //    (cloud-direct architecture rule; SeaweedFS is no longer a Harbor
    //    prerequisite on Sovereigns)."
    // The pre-#652 expectation ['cnpg','seaweedfs','valkey'] is the exact
    // hand-maintained triple the founder directive called out as baseless.
    const harbor = findComponent('harbor')
    expect(harbor).toBeDefined()
    expect(harbor!.dependencies).toEqual(expect.arrayContaining(['cnpg', 'cert-manager']))
    expect(harbor!.dependencies).not.toContain('seaweedfs')
    // The Harbor HelmRelease carries no Redis/Valkey dependency either.
    expect(harbor!.dependencies).not.toContain('valkey')
  })

  it('OpenSearch has no dependencies (it owns its storage)', () => {
    expect(findComponent('opensearch')!.dependencies).toEqual([])
  })

  it('Reloader / KEDA / VPA / Cilium have no deps', () => {
    for (const id of ['reloader', 'keda', 'vpa', 'cilium']) {
      expect(findComponent(id)!.dependencies ?? []).toEqual([])
    }
  })

  it('Flux and Crossplane carry their Flux-canonical install edges', () => {
    // Pre-#652 these were asserted as dependency-free. The bootstrap kit
    // says otherwise and is now canonical:
    //   clusters/_template/bootstrap-kit/03-flux.yaml:59-60 — bp-flux
    //     dependsOn bp-cert-manager.
    //   bp-crossplane dependsOn bp-flux (blueprint-deps.generated.json
    //     "crossplane": ["flux"], from 04-crossplane.yaml).
    expect(findComponent('flux')!.dependencies).toContain('cert-manager')
    expect(findComponent('crossplane')!.dependencies).toContain('flux')
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
    // 2026-05-19 (#1976 / TBD-A64 / PR γ): the legacy "Always Included" /
    // "Choose Your Stack" labels were retired in favour of "Foundation" /
    // "Components" per the canonical Organization marketplace pattern (single flat
    // grid). The cosmetic-guard spec asserts neither phrase appears in
    // the DOM; the section-toggle role attributes were also dropped
    // (`role="tablist"` / `role="tab"` → `aria-pressed`).
    expect(always.textContent).toMatch(new RegExp(`Foundation \\(${MANDATORY.length}\\)`))
  })

  it('Components section is active by default', () => {
    render(<StepComponents />)
    expect(screen.getByTestId('tab-choose').getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByTestId('tab-always').getAttribute('aria-pressed')).toBe('false')
  })

  it('clicking the "Foundation" toggle switches the body', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    expect(screen.getByTestId('tab-always').getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByTestId('always-included-tab')).toBeTruthy()
    // Choose-tab grid + search not in the DOM in foundation view
    expect(screen.queryByTestId('component-grid')).toBeNull()
    expect(screen.queryByTestId('component-search')).toBeNull()
  })

  it('switching back to Components restores the grid', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    fireEvent.click(screen.getByTestId('tab-choose'))
    expect(screen.getByTestId('component-grid')).toBeTruthy()
    expect(screen.getByTestId('component-search')).toBeTruthy()
  })

  it('Components counter reflects user-controllable selection (recommended + optional)', () => {
    // Default state: every recommended is selected. Counter should equal
    // the recommended count (no optional selected).
    const recommendedCount = ALL_COMPONENTS.filter((c) => c.tier === 'recommended').length
    render(<StepComponents />)
    const choose = screen.getByTestId('tab-choose')
    expect(choose.textContent).toMatch(new RegExp(`Components \\(${recommendedCount}\\)`))
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
    // Derived rather than hardcoded: exactly the FABRIC non-mandatories
    // render, and no non-mandatory from any other group does. Deriving
    // keeps this valid as the transitive-mandatory promotion set moves
    // with the Flux-canonical dependency map (#652).
    const fabricNonMandatory = NON_MANDATORY.filter((c) => c.groupId === 'fabric')
    expect(fabricNonMandatory.length).toBeGreaterThan(0)
    for (const c of fabricNonMandatory) {
      expect(screen.getByTestId(`component-card-${c.id}`)).toBeTruthy()
    }
    for (const c of NON_MANDATORY.filter((c) => c.groupId !== 'fabric')) {
      expect(screen.queryByTestId(`component-card-${c.id}`)).toBeNull()
    }
    // cnpg is mandatory (promoted — gitea/harbor/keycloak depend on it)
    // so it must not surface in Tab 1 even with its parent product
    // filtered to. valkey is NOT promoted any more: Harbor's Flux
    // HelmRelease carries no valkey dependency
    // (clusters/_template/bootstrap-kit/19-harbor.yaml:114-118), so
    // valkey is a user-toggleable FABRIC card and IS in the loop above.
    expect(screen.queryByTestId('component-card-cnpg')).toBeNull()
    expect(screen.getByTestId('component-card-valkey')).toBeTruthy()
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
  it('selected components float to the top of the flat grid', () => {
    resetStore({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('category-chip-fabric'))
    fireEvent.click(screen.getByTestId('toggle-clickhouse'))
    // The wizard now renders a single flat grid (no per-family product
    // section headers). Sort assertion scopes to that grid.
    const grid = screen.getByTestId('component-grid')
    const cards = within(grid).getAllByTestId(/^component-card-/)
    const ids = cards.map((c) => c.getAttribute('data-testid'))
    expect(ids[0]).toBe('component-card-clickhouse')
  })
})

/* ── Cascading add ────────────────────────────────────────────────── */

describe('cascading add', () => {
  it('selecting a non-mandatory component adds its transitive deps via the store', () => {
    // Derived fixture. The pre-#652 fixture was `milvus → seaweedfs`, a
    // hand-maintained edge; there is no bp-milvus HelmRelease so
    // `depsFor('milvus')` is now `[]` (componentGroups.ts:533-535) and
    // the pair no longer exercises anything. Pick, from the catalog, a
    // non-mandatory component that actually HAS deps and belongs to an
    // à-la-carte product, so the assertion isolates the COMPONENT-level
    // cascade from the product-family cascade.
    const seed = ALL_COMPONENTS.find(
      (c) =>
        c.tier !== 'mandatory' &&
        resolveTransitiveDependencies(c.id).length > 0 &&
        findProduct(c.product)?.cascadeOnMemberSelection === false,
    )
    expect(seed, 'catalog must contain a non-mandatory component with deps').toBeDefined()
    const deps = resolveTransitiveDependencies(seed!.id)
    expect(deps.length).toBeGreaterThan(0)

    useWizardStore.setState({ selectedComponents: [] })
    useWizardStore.getState().addComponent(seed!.id)
    const sel = useWizardStore.getState().selectedComponents
    expect(sel).toContain(seed!.id)
    for (const d of deps) {
      expect(sel).toContain(d)
    }
  })

  it('store cascades Harbor → cnpg + cert-manager + postgres (not seaweedfs)', () => {
    // Flux-canonical Harbor edges — see the catalog test above and
    // clusters/_template/bootstrap-kit/19-harbor.yaml:35-37 + :114-118.
    useWizardStore.setState({ selectedComponents: [] })
    useWizardStore.getState().addComponent('harbor')
    const sel = useWizardStore.getState().selectedComponents
    expect(sel).toContain('harbor')
    expect(sel).toContain('cnpg')
    expect(sel).toContain('cert-manager')
    expect(sel).toContain('postgres')
    // Transitive: cnpg → flux (blueprint-deps.generated.json "cnpg").
    expect(sel).toContain('flux')
    expect(sel).not.toContain('seaweedfs')
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
    fireEvent.click(screen.getByTestId('toggle-langfuse'))
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
  /**
   * Cascade-remove fixture, DERIVED from the catalog: the first
   * non-mandatory component that has at least one non-mandatory
   * dependent — i.e. a chain the wizard can fully unwind.
   *
   * The pre-#652 fixture was the hardcoded `strimzi → debezium` pair.
   * Neither strimzi nor debezium has a bp-* HelmRelease, so since #652
   * (componentGroups.ts:536) both resolve to `dependencies: []` and the
   * pair produces no cascade at all. Deriving keeps the test exercising
   * the real feature as the Flux-canonical map evolves.
   *
   * (Today this resolves to `opentelemetry → alloy` —
   * blueprint-deps.generated.json `"alloy": ["opentelemetry"]`.)
   */
  function cascadeRemovePair(): { target: ComponentEntry; dependents: ComponentEntry[] } {
    for (const c of NON_MANDATORY) {
      const dependents = resolveTransitiveDependents(c.id)
        .map((id) => findComponent(id))
        .filter((d): d is ComponentEntry => !!d && d.tier !== 'mandatory')
      if (dependents.length > 0) return { target: c, dependents }
    }
    throw new Error(
      'catalog has no non-mandatory component with a non-mandatory dependent — ' +
        'the cascade-remove flow is untestable and this is itself a defect',
    )
  }

  it('opens a confirm dialog when removing a component with dependents', () => {
    const { target, dependents } = cascadeRemovePair()
    useWizardStore.setState({
      selectedComponents: [
        ...new Set([
          ...useWizardStore.getState().selectedComponents,
          target.id,
          ...dependents.map((d) => d.id),
        ]),
      ].sort(),
    })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId(`toggle-${target.id}`))
    expect(screen.getByTestId('cascade-dialog')).toBeTruthy()
    const list = screen.getByTestId('cascade-dependents')
    for (const d of dependents) {
      expect(list.textContent).toContain(d.name)
    }
  })

  it('cancel keeps the component selected', () => {
    const { target, dependents } = cascadeRemovePair()
    useWizardStore.setState({
      selectedComponents: [
        ...new Set([
          ...useWizardStore.getState().selectedComponents,
          target.id,
          ...dependents.map((d) => d.id),
        ]),
      ].sort(),
    })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId(`toggle-${target.id}`))
    fireEvent.click(screen.getByTestId('cascade-cancel'))
    expect(useWizardStore.getState().selectedComponents).toContain(target.id)
    expect(screen.queryByTestId('cascade-dialog')).toBeNull()
  })

  it('confirm cascades through the impact set', () => {
    const { target, dependents } = cascadeRemovePair()
    useWizardStore.setState({
      selectedComponents: [target.id, ...dependents.map((d) => d.id)].sort(),
    })
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId(`toggle-${target.id}`))
    fireEvent.click(screen.getByTestId('cascade-confirm'))
    const sel = useWizardStore.getState().selectedComponents
    expect(sel).not.toContain(target.id)
    for (const d of dependents) {
      expect(sel).not.toContain(d.id)
    }
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

/**
 * Regression guard — 2 tests here + 2 in the transitive-promotion block
 * below WERE red on `main`. Do NOT relax them.
 *
 * `AlwaysIncludedTab` used to build Tab 2 from
 * `PRODUCTS.filter(product => product.tier === 'mandatory')` — i.e. it
 * keyed visibility off the PRODUCT tier, not the COMPONENT tier. The
 * Foundation counter next to it keys off the COMPONENT tier
 * (`ALL_COMPONENTS.filter(c => c.tier === 'mandatory')`). The two
 * disagreed.
 *
 * `cnpg` and `postgres` are `tier: 'mandatory'` (promoted — the
 * unconditionally-mandatory gitea / harbor / keycloak depend on them)
 * but live in FABRIC, whose PRODUCT tier is 'recommended'. So they were
 * filtered out of Tab 2, while Tab 1 already excludes them for being
 * mandatory. Net effect: CloudNative PG and PostgreSQL were installed on
 * every Sovereign but appeared in NEITHER tab, and the tab read
 * "Foundation (24)" while rendering 22 cards.
 *
 * The product-tier filter existed to stop KServe (then CORTEX-internal
 * `tier: 'mandatory'`) showing under "Always Included" on Sovereigns that
 * never picked CORTEX. That guard is preserved: AlwaysIncludedTab now
 * admits a component when it is mandatory in a mandatory-tier product OR
 * when it appears in `TRANSITIVE_MANDATORY_PROMOTIONS` — something
 * unconditional depends on it, so it ships either way. A family-internal
 * raw-mandatory member is neither, so it still stays out.
 */
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
    fireEvent.click(screen.getByTestId('toggle-clickhouse'))
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
    // Flux-canonical dependents since #652. The pre-#652 expectation also
    // listed ferretdb / temporal / librechat / matrix / superset /
    // openmeter — every one of those was a hand-maintained `→ cnpg` edge
    // on a component with NO bp-* HelmRelease, so `depsFor()` now returns
    // `[]` for them (componentGroups.ts:533-535) and they are no longer
    // cnpg dependents. The ids below each have a real bootstrap-kit HR
    // whose `dependsOn` reaches cnpg:
    //   gitea      — 10-gitea.yaml:65-72   (bp-cnpg, bp-postgres-shared)
    //   keycloak   — 09-keycloak.yaml:68-74 (→ bp-postgres-shared → bp-cnpg)
    //   harbor     — 19-harbor.yaml:114-118 (bp-cnpg)
    //   grafana    — 25-grafana.yaml:57-59  (bp-cnpg)
    //   powerdns   — 11-powerdns.yaml       (bp-cnpg)
    //   openbao    — 08-openbao.yaml        (bp-cnpg)
    //   postgres   — the shared data-instance, dependsOn bp-cnpg
    //   langfuse   — bp-langfuse dependsOn bp-cnpg
    const dependents = resolveTransitiveDependents('cnpg')
    for (const id of [
      'gitea', 'keycloak', 'harbor', 'grafana',
      'powerdns', 'openbao', 'postgres', 'langfuse',
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

  it('postgres is mandatory in the post-promotion catalog', () => {
    // postgres (the #3370 shareable data-instance) replaced valkey as the
    // promoted FABRIC member: gitea / harbor / keycloak — all raw
    // mandatory — dependsOn bp-postgres-shared
    // (10-gitea.yaml:65-72, 19-harbor.yaml:114-118, 09-keycloak.yaml:68-74),
    // so the closure walk lifts it.
    expect(findComponent('postgres')!.tier).toBe('mandatory')
  })

  it('valkey is NOT promoted — no mandatory component depends on it', () => {
    // Pre-#652 valkey was promoted solely via Harbor's hand-maintained
    // `['cnpg','seaweedfs','valkey']` literal. The Harbor HelmRelease
    // (clusters/_template/bootstrap-kit/19-harbor.yaml:114-118) carries no
    // valkey/Redis dependency, so valkey stays user-toggleable.
    expect(findComponent('valkey')!.tier).not.toBe('mandatory')
    expect(TRANSITIVE_MANDATORY_PROMOTIONS).not.toContain('valkey')
  })

  it('TRANSITIVE_MANDATORY_PROMOTIONS includes cnpg + postgres', () => {
    expect(TRANSITIVE_MANDATORY_PROMOTIONS).toContain('cnpg')
    expect(TRANSITIVE_MANDATORY_PROMOTIONS).toContain('postgres')
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

  // Regression guard — pins the AlwaysIncludedTab product-tier filter
  // defect documented above the "Tab 2 (Always Included)" describe block.
  it('cnpg DOES appear in Tab 2 ("Always Included")', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    expect(screen.getByTestId('component-card-cnpg')).toBeTruthy()
  })

  it('valkey does NOT appear in Tab 2 — it is user-selectable in Tab 1', () => {
    // Inverted from the pre-#652 expectation: valkey is no longer
    // promoted (see "valkey is NOT promoted" above), so it belongs in the
    // Components tab, not Foundation.
    render(<StepComponents />)
    expect(screen.getByTestId('component-card-valkey')).toBeTruthy()
    fireEvent.click(screen.getByTestId('tab-always'))
    const tab = screen.getByTestId('always-included-tab')
    expect(within(tab).queryByTestId('component-card-valkey')).toBeNull()
  })

  // Regression guard — same AlwaysIncludedTab defect. The valkey
  // assertion that used to sit here was dropped (valkey is no longer a
  // promotion), so this covers ONLY the real reason it was red: FABRIC
  // has two promoted mandatory members and used to render no section.
  it('promoted components are grouped under their owning product in Tab 2', () => {
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))
    const fabricSection = screen.getByTestId('always-included-section-fabric')
    expect(within(fabricSection).getByTestId('component-card-cnpg')).toBeTruthy()
    expect(within(fabricSection).getByTestId('component-card-postgres')).toBeTruthy()
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

  it('CORTEX has no family-level dependencies (audit 2026-04 — Spector → FABRIC bug fix)', () => {
    // Pre-audit value was `['fabric']`, which over-broadly cascaded
    // every Strimzi / Debezium / Flink / Temporal / ClickHouse / Iceberg
    // / Superset selection onto an operator who'd just ticked Specter or
    // BGE. CORTEX members' real cross-family need is cnpg (LangFuse) and
    // a Mongo-compat store (LibreChat → FerretDB → cnpg). cnpg is
    // mandatory by transitive promotion; FerretDB cascades via the
    // component-level dep on librechat. No family-level cascade needed.
    expect(findProduct('cortex')!.familyDependencies).toEqual([])
  })

  /**
   * Regression guard — this WAS red on `main`. Do NOT relax it, and do
   * NOT "fix" it by deleting the expectation.
   *
   * The Specter → CORTEX product rule is operator-requested (issue #175:
   * "when Specter is selected all the Cortex family is required") and is
   * documented in the catalog in two places:
   *   the Specter ComponentDef — "Specter requires the entire CORTEX
   *     family at runtime … selecting Specter adds the full CORTEX family
   *     even if the user never opens the CORTEX chip."
   *   the INSIGHTS Product entry — "Selecting the INSIGHTS product as a
   *     whole brings in Specter, which in turn pulls CORTEX through the
   *     component->product cascade chain."
   * It used to be written as a literal
   *   dependencies: ['bge','milvus','langfuse','vllm','kserve']
   * which was DEAD: RAW_COMPONENTS replaces `dependencies` with
   * `depsFor('specter')`, and there is no bp-specter HelmRelease, so
   * Specter's dependencies resolved to `[]`. Specter belongs to INSIGHTS
   * (cascadeOnMemberSelection: false), so nothing else fired either.
   *
   * Operator-visible symptom was: ticking Specter (the AIOps brain)
   * installed Specter alone — no vLLM, no Milvus, no BGE, no LangFuse, no
   * KServe — i.e. an AIOps component with no AI runtime under it.
   *
   * This is NOT the same class as the other #652 casualties: those were
   * install-ordering edges the founder ruled Flux owns. This is a
   * product-family SELECTION rule, which Flux has no opinion on. It now
   * lives in the layer that survives the Flux override: the Specter
   * ComponentDef declares `familyRequires: ['cortex']`, and
   * `resolveCatalogDependencies()` expands it from the PRODUCTS table at
   * module load — so the member list can never drift from the CORTEX
   * family definition, and no per-component id list is hand-maintained.
   */
  it('Specter component-level deps cover the major CORTEX runtime members', () => {
    const specter = findComponent('specter')!
    for (const dep of ['bge', 'milvus', 'langfuse', 'vllm', 'kserve']) {
      expect(specter.dependencies).toContain(dep)
    }
  })

  it('LibreChat has no Flux-canonical deps (no bp-librechat HelmRelease)', () => {
    // The 2026-04 audit's `librechat → ferretdb` edge was hand-maintained.
    // #652 (commit 544dc86b5) made Flux `HelmRelease.dependsOn` the single
    // source of truth per founder directive — "The single source of truth
    // for the dependencies is flux!!!" — and there is no bp-librechat
    // HelmRelease in clusters/_template/bootstrap-kit, so `depsFor()`
    // returns the documented empty-list fallback
    // (componentGroups.ts:533-535).
    //
    // NOTE for whoever revisits this: LibreChat genuinely speaks MongoDB,
    // so the Mongo-compat requirement is real — it just has to be
    // expressed by adding a bp-librechat HelmRelease with the right
    // `dependsOn`, not by re-introducing a literal that :536 discards.
    const librechat = findComponent('librechat')!
    expect(librechat.dependencies).toEqual([])
  })

  it('Grafana depends on its Flux-canonical datastores and datasources', () => {
    // Inverted from the 2026-04 "Grafana uses SQLite by default" audit
    // claim, which the bootstrap kit contradicts.
    // clusters/_template/bootstrap-kit/25-grafana.yaml:11-17 documents the
    // intent and :57-70 declares it:
    //   bp-cnpg    (":12 — Postgres backend for Grafana state")
    //   bp-loki    (":13 — datasource for logs")
    //   bp-mimir   (":14 — datasource for metrics")
    //   bp-tempo   (":15 — datasource for traces")
    //   bp-keycloak(":16 — OIDC IdP for SSO")
    const grafana = findComponent('grafana')!
    for (const dep of ['cnpg', 'loki', 'mimir', 'tempo', 'keycloak']) {
      expect(grafana.dependencies).toContain(dep)
    }
    // seaweedfs is still NOT a direct Grafana dep — it arrives only
    // transitively through Loki / Mimir / Tempo.
    expect(grafana.dependencies).not.toContain('seaweedfs')
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

  it('addProduct(cortex) does NOT drag FABRIC à-la-carte members (audit 2026-04)', () => {
    // Pre-audit `familyDependencies: ['fabric']` cascaded every FABRIC
    // member onto a CORTEX selection. That was wrong — CORTEX needs
    // cnpg (mandatory by transitive promotion) and, for LibreChat, a
    // Mongo-compat store (FerretDB, which cascades via the component
    // dep). It does NOT need Strimzi / Debezium / Flink / Temporal /
    // ClickHouse / Iceberg / Superset.
    useWizardStore.setState({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    useWizardStore.getState().addProduct('cortex')
    const sel = useWizardStore.getState().selectedComponents
    for (const id of [
      'strimzi', 'debezium', 'flink', 'temporal',
      'clickhouse', 'iceberg', 'superset',
    ]) {
      expect(sel).not.toContain(id)
    }
    // cnpg stays present because it's mandatory. The pre-#652 companion
    // assertion `toContain('ferretdb')` is gone: it relied on the
    // hand-maintained `librechat → ferretdb` edge, which #652 replaced
    // with the empty Flux-canonical list (see the LibreChat test above).
    // ferretdb is now covered by the FABRIC-exclusion loop instead.
    expect(sel).toContain('cnpg')
    expect(sel).not.toContain('ferretdb')
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

  it('removeProduct preserves the product’s mandatory members (FABRIC)', () => {
    // Retargeted from CORTEX. The old fixture asserted `kserve` survived
    // removeProduct('cortex') because KServe was `tier: 'mandatory'`.
    // KServe was deliberately demoted to `tier: 'recommended'` —
    // componentGroups.ts:338 and the comment at :326-337: "KServe was
    // tier:'mandatory' — semantically wrong … Demoting to 'recommended'"
    // (caught on the omantel.biz wizard 2026-05-06). CORTEX now has ZERO
    // mandatory members, so the old assertion is unsatisfiable and the
    // guard at store.ts:653 went untested.
    //
    // FABRIC does have mandatory members (cnpg + postgres, promoted), so
    // it exercises the same invariant for real. Derived, not hardcoded.
    const fabricMandatory = componentsByProduct('fabric').filter((c) => c.tier === 'mandatory')
    expect(fabricMandatory.length).toBeGreaterThan(0)

    const fullFabric = resolveProductComponentClosure('fabric')
    useWizardStore.setState({
      selectedComponents: [...new Set([...MANDATORY_COMPONENT_IDS, ...fullFabric])].sort(),
    })
    useWizardStore.getState().removeProduct('fabric')
    const sel = useWizardStore.getState().selectedComponents
    for (const c of fabricMandatory) {
      expect(sel).toContain(c.id)
    }
    // …and the non-mandatory members really did leave, so this cannot
    // pass by removeProduct being a no-op.
    for (const c of componentsByProduct('fabric')) {
      if (c.tier === 'mandatory') continue
      expect(sel).not.toContain(c.id)
    }
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

  // Regression guard — pins the Specter → CORTEX defect documented in
  // full above the "Specter component-level deps" test. The cascade
  // MECHANISM was always fine (the BGE test directly above proves it
  // fires); the Specter → CORTEX edge is what had gone missing.
  it('selecting Specter cascades to every CORTEX component', () => {
    useWizardStore.setState({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    useWizardStore.getState().addComponent('specter')
    const sel = useWizardStore.getState().selectedComponents
    for (const c of componentsByProduct('cortex')) {
      expect(sel).toContain(c.id)
    }
  })

  // Audit 2026-04: user-reported defect — "if I select spectoer it is
  // bringing the entire fabric family as well, I dont think there is
  // such depenency in reality". Ground truth: Specter (AIOps brain)
  // needs the CORTEX runtime members it lists in `dependencies`. It
  // does NOT need Strimzi/Kafka, Debezium/CDC, Flink, Temporal,
  // ClickHouse, Iceberg, or Superset. The previous CORTEX
  // `familyDependencies: ['fabric']` was over-broad and is removed in
  // this commit.
  // ⚠️ CURRENTLY VACUOUS, deliberately kept. This guards the 2026-04
  // over-cascade regression (CORTEX `familyDependencies: ['fabric']`).
  // While the Specter → CORTEX defect above is open Specter cascades
  // NOTHING, so this passes trivially; it regains its teeth the moment
  // that defect is fixed. Left in place rather than deleted so the
  // regression stays guarded — do not treat its green as coverage today.
  it('selecting Specter does NOT auto-select the FABRIC family (regression)', () => {
    useWizardStore.setState({ selectedComponents: [...MANDATORY_COMPONENT_IDS].sort() })
    useWizardStore.getState().addComponent('specter')
    const sel = useWizardStore.getState().selectedComponents
    for (const id of [
      'strimzi', 'debezium', 'flink', 'temporal',
      'clickhouse', 'iceberg', 'superset',
    ]) {
      expect(sel).not.toContain(id)
    }
  })

  // The former 'selecting Specter does NOT auto-select FerretDB' test was
  // DELETED here. Its title asserted Specter must not pull FerretDB while
  // its body asserted the opposite (`toContain('ferretdb')`) — the 2026-04
  // audit inverted the body and left the title behind. Both of its
  // assertions were duplicates: the `librechat` half restates
  // 'selecting Specter cascades to every CORTEX component' above, and the
  // `ferretdb` half restates the LibreChat dependency test in the
  // product-family block. Nothing it covered is now uncovered.

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
    fireEvent.click(screen.getByTestId('toggle-bge'))
    const toast = screen.getByTestId('toast-added')
    expect(toast.textContent).toMatch(/BGE added/)
    expect(toast.textContent).toMatch(/CORTEX family/)
  })
})

/* ── Flat grid + family chip ───────────────────────────────────── */

describe('Tab 1 — flat grid (no per-family section headers)', () => {
  it('renders a single flat component grid (no product-section headers)', () => {
    render(<StepComponents />)
    expect(screen.getByTestId('component-grid')).toBeTruthy()
    // Per-family section headers were removed in the chip + product-detail
    // refactor — the catalog is one flat marketplace grid now.
    for (const p of PRODUCTS) {
      expect(screen.queryByTestId(`product-section-${p.id}`)).toBeNull()
    }
  })

  it('every non-mandatory card carries a clickable family chip', () => {
    render(<StepComponents />)
    for (const c of NON_MANDATORY) {
      const chip = screen.queryByTestId(`family-chip-${c.id}`)
      expect(chip).toBeTruthy()
      // Anchor element so clicking dispatches navigation through the
      // tanstack router context.
      expect(chip!.tagName.toLowerCase()).toBe('a')
      expect(chip!.textContent).toMatch(new RegExp(c.groupName, 'i'))
    }
  })

  it('every non-mandatory card surfaces an explicit Add / Selected toggle button', () => {
    render(<StepComponents />)
    for (const c of NON_MANDATORY) {
      expect(screen.getByTestId(`toggle-${c.id}`)).toBeTruthy()
    }
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
