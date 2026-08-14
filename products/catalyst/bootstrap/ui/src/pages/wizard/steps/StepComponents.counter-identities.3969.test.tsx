/**
 * StepComponents.counter-identities.3969.test.tsx — UAT row W5, the COUNTS
 * half of the clause: *"Component-selection step counts are self-consistent
 * and every component id resolves to a real Blueprint."*
 *
 * ── Why a separate file, and why it reads the DOM ────────────────────
 *
 * The live walk of wizard step 4 read `Selected (43) of 59`, `Components
 * (20)`, `Foundation (23)`. Those three numbers come from three different
 * expressions in StepComponents.tsx and are rendered next to a grid built by
 * a fourth. Nothing joined them:
 *
 *   • `Foundation (n)`  ← `ALL_COMPONENTS.filter(tier === 'mandatory').length`
 *   • `Components (n)`  ← selected ids whose tier is not mandatory
 *   • `Selected (a) of b` ← `store.selectedComponents.length` / `ALL_COMPONENTS.length`
 *   • the two grids     ← `choosePool` and `AlwaysIncludedTab`'s own filter
 *
 * They HAVE disagreed. The comment at StepComponents.tsx:645-653 records the
 * counter reading "Foundation (24)" over 22 rendered cards, because the tab
 * filter asked about the PRODUCT tier while the counter asked about the
 * COMPONENT tier — {cnpg, postgres} fell between the two and appeared in
 * neither grid while shipping on every Sovereign.
 *
 * So every assertion here measures the RENDERED DOM against another RENDERED
 * number, or against a value derived from the catalog — never against a
 * literal. A test that hardcoded 58 / 35 / 23 would have to be edited by the
 * next person who adds a component, and would prove only that two copies of
 * the same list agree with each other.
 *
 * The removal of the `specter` card (#6183) moves the denominator 59 → 58 and
 * the Components pool 36 → 35. It must move NEITHER of the other two: the
 * card was `tier: 'optional'`, so it was never in Foundation and never in the
 * default selection. That asymmetry is asserted directly — it is what makes
 * "the counts still add up" a real claim rather than a restatement.
 */

import { describe, it, expect, beforeAll, beforeEach, afterEach } from 'vitest'
import {
  render as rtlRender,
  screen,
  fireEvent,
  cleanup,
  act,
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
  findComponent,
  computeDefaultSelection,
  PRODUCTS,
} from './componentGroups'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

/* ── Router harness (mirrors StepComponents.test.tsx) ─────────────── */

let TEST_UI_SLOT: React.ReactNode = null

const rootRoute = createRootRoute({ component: () => <Outlet /> })
const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  component: () => <>{TEST_UI_SLOT}</>,
})
const familyRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/marketplace/family/$familyId',
  component: () => <div data-testid="stub-marketplace-family" />,
})
const productRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/marketplace/product/$componentId',
  component: () => <div data-testid="stub-marketplace-product" />,
})
const router = createRouter({
  routeTree: rootRoute.addChildren([indexRoute, familyRoute, productRoute]),
  history: createMemoryHistory({ initialEntries: ['/'] }),
})

beforeAll(async () => {
  await router.load()
})

function render(ui: React.ReactElement): RenderResult {
  TEST_UI_SLOT = ui
  return rtlRender(<RouterProvider router={router} />)
}

beforeEach(() => {
  useWizardStore.setState({
    ...INITIAL_WIZARD_STATE,
    selectedComponents: [...computeDefaultSelection()].sort(),
  })
})

afterEach(async () => {
  cleanup()
  TEST_UI_SLOT = null
  if (router.state.location.pathname !== '/') {
    await act(async () => {
      await router.navigate({ to: '/' })
    })
  }
})

/* ── DOM readers ──────────────────────────────────────────────────── */

/** Pull the integer out of a counter label like `Foundation (23)`. */
function counterValue(testId: string): number {
  const text = screen.getByTestId(testId).textContent ?? ''
  const m = text.match(/\((\d+)\)/)
  if (!m) throw new Error(`no "(n)" found in ${testId}: ${JSON.stringify(text)}`)
  return Number(m[1])
}

/** `Selected (a) of b` → [a, b]. */
function selectedCounterPair(): [number, number] {
  const text = screen.getByTestId('selected-counter').textContent ?? ''
  const m = text.match(/\((\d+)\)\s*of\s*(\d+)/)
  if (!m) throw new Error(`selected-counter did not match: ${JSON.stringify(text)}`)
  return [Number(m[1]), Number(m[2])]
}

/** Component ids of every card currently in the DOM. */
function renderedCardIds(): string[] {
  return Array.from(document.querySelectorAll('[data-testid^="component-card-"]')).map((el) =>
    (el.getAttribute('data-testid') ?? '').replace(/^component-card-/, ''),
  )
}

/** Ids of the rendered cards marked selected. */
function renderedSelectedCardIds(): string[] {
  return Array.from(document.querySelectorAll('[data-testid^="component-card-"]'))
    .filter((el) => el.getAttribute('data-selected') === 'true')
    .map((el) => (el.getAttribute('data-testid') ?? '').replace(/^component-card-/, ''))
}

/* ── The identities ───────────────────────────────────────────────── */

describe('UAT row W5 — step-4 counter identities (#3969)', () => {
  it('Foundation (n) equals the number of cards the Foundation grid renders', () => {
    // The identity that has actually broken: "Foundation (24)" over 22 cards.
    render(<StepComponents />)
    fireEvent.click(screen.getByTestId('tab-always'))

    const label = counterValue('tab-always')
    const cards = renderedCardIds()
    expect(cards.length).toBe(label)
    // Vacuity — a grid that rendered nothing would satisfy 0 === 0 if the
    // counter were also broken to 0.
    expect(cards.length).toBeGreaterThan(10)
  })

  it('Components (n) equals the number of SELECTED cards the Components grid renders', () => {
    render(<StepComponents />)
    // 'choose' is the default tab; assert that rather than assume it.
    expect(screen.getByTestId('tab-choose').getAttribute('aria-pressed')).toBe('true')

    const label = counterValue('tab-choose')
    const selectedCards = renderedSelectedCardIds()
    expect(selectedCards.length).toBe(label)
    expect(selectedCards.length).toBeGreaterThan(5)
    // …and the grid holds strictly more than the selected subset, so the
    // equality above is not the trivial "everything is selected" case.
    expect(renderedCardIds().length).toBeGreaterThan(selectedCards.length)
  })

  it('the denominator equals Foundation cards + Components cards, measured across both tabs', () => {
    // THE cross-tab identity: `Selected (a) of b` where b is rendered on the
    // Components tab but must account for the Foundation tab too. Both
    // operands are counted from the DOM of the tab that owns them.
    render(<StepComponents />)
    const [, denominator] = selectedCounterPair()
    const chooseCards = renderedCardIds().length

    fireEvent.click(screen.getByTestId('tab-always'))
    const foundationCards = renderedCardIds().length

    expect(foundationCards + chooseCards).toBe(denominator)
    expect(chooseCards).toBeGreaterThan(10)
    expect(foundationCards).toBeGreaterThan(10)
  })

  it('the two tabs share zero component ids', () => {
    render(<StepComponents />)
    const chooseIds = new Set(renderedCardIds())

    fireEvent.click(screen.getByTestId('tab-always'))
    const foundationIds = renderedCardIds()

    const overlap = foundationIds.filter((id) => chooseIds.has(id))
    expect(overlap).toEqual([])
    // Vacuity — an empty Foundation grid would trivially overlap with nothing.
    expect(foundationIds.length).toBeGreaterThan(10)
    expect(chooseIds.size).toBeGreaterThan(10)
  })

  it('Selected (a) equals Foundation (n) + Components (n)', () => {
    // `Selected (43)` = 23 mandatory + 20 recommended, from the live walk.
    // Read as three DOM numbers so the identity holds at whatever the
    // catalog size becomes.
    render(<StepComponents />)
    const [selected] = selectedCounterPair()
    const components = counterValue('tab-choose')
    const foundation = counterValue('tab-always')

    expect(foundation + components).toBe(selected)
    expect(components).toBeGreaterThan(0)
    expect(foundation).toBeGreaterThan(0)
  })

  it('the selected count on a fresh wizard is exactly mandatory + recommended', () => {
    // Derived from the catalog, not from a literal: an optional card must
    // never arrive pre-selected. This is the assertion the `specter` removal
    // must NOT move — it was optional, so 43 stays 43 while 59 becomes 58.
    render(<StepComponents />)
    const [selected] = selectedCounterPair()

    const expected = ALL_COMPONENTS.filter(
      (c) => c.tier === 'mandatory' || c.tier === 'recommended',
    ).length
    expect(selected).toBe(expected)

    const optionalSelected = useWizardStore
      .getState()
      .selectedComponents.filter((id) => findComponent(id)?.tier === 'optional')
    expect(optionalSelected).toEqual([])
  })

  it('the denominator equals the catalog size and every rendered id is a real component', () => {
    render(<StepComponents />)
    const [, denominator] = selectedCounterPair()
    expect(denominator).toBe(ALL_COMPONENTS.length)

    const chooseIds = renderedCardIds()
    fireEvent.click(screen.getByTestId('tab-always'))
    const allIds = [...chooseIds, ...renderedCardIds()]

    expect(allIds.length).toBe(ALL_COMPONENTS.length)
    for (const id of allIds) {
      expect(findComponent(id), `${id} has no ComponentDef`).toBeTruthy()
    }
  })

  /* ── The removed card ─────────────────────────────────────────── */

  it('specter is offered by neither tab and has no ComponentDef (#6183)', () => {
    expect(findComponent('specter')).toBeUndefined()
    expect(ALL_COMPONENTS.map((c) => c.id)).not.toContain('specter')
    expect(PRODUCTS.flatMap((p) => p.components)).not.toContain('specter')

    render(<StepComponents />)
    expect(screen.queryByTestId('component-card-specter')).toBeNull()
    fireEvent.click(screen.getByTestId('tab-always'))
    expect(screen.queryByTestId('component-card-specter')).toBeNull()
  })

  it('adding specter is a no-op — it cannot re-enter the payload by id (#6183)', () => {
    // The card is gone from the grid, but `addComponent` is also reachable
    // from the marketplace product page's Select CTA and from any persisted
    // payload path. An unknown id must change nothing — including not
    // dragging the nine CORTEX components its `familyRequires: ['cortex']`
    // used to cascade.
    const before = [...useWizardStore.getState().selectedComponents]
    useWizardStore.getState().addComponent('specter')
    expect(useWizardStore.getState().selectedComponents).toEqual(before)

    // Control — a real optional id DOES change the selection, so the
    // assertion above is not passing on a store that ignores every add.
    useWizardStore.getState().addComponent('litmus')
    expect(useWizardStore.getState().selectedComponents).toContain('litmus')
  })
})
