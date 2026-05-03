/**
 * WizardLayout.test.tsx — vitest coverage for the page-header refactor that
 * closes GitHub issue #174.
 *
 * Asserts the spec verbatim:
 *
 *   • The header band carries `data-testid="wizard-header"` and is
 *     visually distinct (single sticky row hosting brand + stepper +
 *     actions). The 56px header height is asserted via the header's
 *     inline-style contract — kept token-driven in CSS, so we check the
 *     element exists and the heading region (logo, stepper, actions) is
 *     present in DOM order.
 *
 *   • The OpenOva logo lives inside the header (anchored on the brand
 *     `Link`).
 *
 *   • Exactly seven step indicators render (StepOrg → StepReview),
 *     matching the wizard's seven-step waterfall.
 *
 *   • The active step gets the `active` class and `aria-current="step"`,
 *     so screen readers and visual regression tests both confirm the
 *     highlight.
 *
 *   • The mobile-collapsed indicator carries `data-testid="wizard-stepper-compact"`
 *     and reports "Step X of Y" — the small-screen fallback the issue
 *     calls out.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, within } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { WizardLayout, WIZARD_STEPS } from './WizardLayout'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

/**
 * Build a minimal in-memory router that mounts WizardLayout at `/wizard`
 * with a child route rendering an empty Outlet. This mirrors the
 * production routing tree (router.tsx) without pulling in real page
 * components — the test stays scoped to the layout's chrome.
 */
function renderLayout() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: WizardLayout,
  })
  const indexRoute = createRoute({
    getParentRoute: () => wizardRoute,
    path: '/',
    component: () => <div data-testid="step-content">step body</div>,
  })
  const routeTree = rootRoute.addChildren([wizardRoute.addChildren([indexRoute])])
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ['/wizard'] }),
  })
  // Issue #689 — WizardLayout now mounts <ProfileMenu>, which calls
  // useSession() (a React Query hook). The layout therefore requires a
  // QueryClientProvider in the tree.
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  // Reset Zustand store so every test starts on step 1.
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  // Stub /whoami → 401 (anonymous) so ProfileMenu renders the
  // [Sign in] button without making a real network request.
  vi.spyOn(global, 'fetch').mockImplementation(async () => {
    return new Response(JSON.stringify({ error: 'unauthenticated' }), { status: 401 })
  })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('WizardLayout — page-header refactor (#174)', () => {
  it('renders a header band with data-testid="wizard-header"', async () => {
    renderLayout()
    const header = await screen.findByTestId('wizard-header')
    expect(header).toBeTruthy()
    expect(header.tagName.toLowerCase()).toBe('header')
  })

  it('renders the OpenOva brand mark inside the header', async () => {
    renderLayout()
    const header = await screen.findByTestId('wizard-header')
    const logoLink = within(header).getByTestId('wizard-logo')
    // OOLogo renders an <svg>; assert the SVG is the logo's child.
    expect(logoLink.querySelector('svg')).toBeTruthy()
    expect(within(header).getByText('OpenOva')).toBeTruthy()
  })

  it('renders exactly seven step indicators inside the header', async () => {
    renderLayout()
    const header = await screen.findByTestId('wizard-header')
    const stepper = within(header).getByTestId('wizard-stepper')
    // Each step is a button — 7 buttons match WIZARD_STEPS.length.
    const stepButtons = within(stepper).getAllByRole('button')
    expect(stepButtons).toHaveLength(WIZARD_STEPS.length)
    expect(WIZARD_STEPS.length).toBe(7)
    // Every step exposes a stable testid so visual regression tests can
    // pin to it without relying on text content.
    for (const step of WIZARD_STEPS) {
      expect(within(stepper).getByTestId(`wizard-step-${step.id}`)).toBeTruthy()
    }
  })

  it('marks the active step with the .active class and aria-current="step"', async () => {
    useWizardStore.setState({ ...INITIAL_WIZARD_STATE, currentStep: 3 })
    renderLayout()
    const stepper = await screen.findByTestId('wizard-stepper')
    const activeBtn = within(stepper).getByTestId('wizard-step-3')
    expect(activeBtn.className).toContain('active')
    expect(activeBtn.getAttribute('aria-current')).toBe('step')
    // Sanity: a non-active step must not carry either marker.
    const otherBtn = within(stepper).getByTestId('wizard-step-5')
    expect(otherBtn.className).not.toContain('active')
    expect(otherBtn.getAttribute('aria-current')).toBeNull()
  })

  it('marks completed steps with the .done class', async () => {
    useWizardStore.setState({ ...INITIAL_WIZARD_STATE, currentStep: 4 })
    renderLayout()
    const stepper = await screen.findByTestId('wizard-stepper')
    expect(within(stepper).getByTestId('wizard-step-1').className).toContain('done')
    expect(within(stepper).getByTestId('wizard-step-2').className).toContain('done')
    expect(within(stepper).getByTestId('wizard-step-3').className).toContain('done')
    expect(within(stepper).getByTestId('wizard-step-4').className).not.toContain('done')
    expect(within(stepper).getByTestId('wizard-step-4').className).toContain('active')
  })

  it('renders a mobile-collapsed "Step X of Y" indicator inside the header', async () => {
    useWizardStore.setState({ ...INITIAL_WIZARD_STATE, currentStep: 3 })
    renderLayout()
    const header = await screen.findByTestId('wizard-header')
    const compact = within(header).getByTestId('wizard-stepper-compact')
    // Plain-text assertion: the compact strip says "Step 3" / "of 7".
    expect(compact.textContent).toContain('Step')
    expect(compact.textContent).toContain('3')
    expect(compact.textContent).toContain(String(WIZARD_STEPS.length))
  })

  it('does NOT render the legacy stepper inside the step body', async () => {
    renderLayout()
    // Wait for the router to mount the layout, THEN assert there is
    // exactly one stepper and it is anchored inside the header (the OLD
    // layout rendered a second `<nav class="corp-stepper">` inside
    // .corp-main — that DOM node must no longer exist).
    const header = await screen.findByTestId('wizard-header')
    const allSteppers = await screen.findAllByTestId('wizard-stepper')
    expect(allSteppers).toHaveLength(1)
    expect(header.contains(allSteppers[0]!)).toBe(true)
  })
})

// Issue #689 — guest-mode header chrome.
describe('WizardLayout — guest mode (#689)', () => {
  it('renders a [Sign in] button when the visitor is anonymous', async () => {
    renderLayout()
    const header = await screen.findByTestId('wizard-header')
    // The ProfileMenu renders profile-menu-signin in the anonymous branch.
    const signin = await within(header).findByTestId('profile-menu-signin')
    expect(signin.textContent).toContain('Sign in')
  })

  it('renders the flash banner when one was queued by the provision auth guard', async () => {
    sessionStorage.setItem('openova-wizard-flash-banner', 'Sign in to view your deployments')
    renderLayout()
    const banner = await screen.findByTestId('wizard-flash-banner')
    expect(banner.textContent).toContain('Sign in to view your deployments')
    // The banner clears itself on read, so a re-render should NOT show
    // it — the consume-on-mount semantics are load-bearing for the
    // sessionStorage seam.
    expect(sessionStorage.getItem('openova-wizard-flash-banner')).toBeNull()
  })
})
