/**
 * SettingsPage.test.tsx — wiring lock-in for the Sovereign Settings
 * page (issue #516).
 *
 * Coverage:
 *   • Page renders inside PortalShell with the canonical sidebar +
 *     header chrome.
 *   • All sections are present (organization, sovereign, api-tokens,
 *     cloud-credentials, dns, domain-mode, parent-domains [#4089],
 *     marketplace, notifications, members, danger-zone).
 *   • In-page TOC entries point at the matching anchors.
 *   • Sovereign info reflects the live snapshot fields (FQDN, region,
 *     deployment id) — the page is not a static placeholder.
 *   • Org info reflects the wizard store fields.
 *   • API tokens render the canonical 5 service kinds.
 *   • Members section links to the existing User Access page.
 *   • Danger zone exposes the decommission link.
 *   • The page does NOT redirect to /wizard (regression guard for the
 *     original bug).
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { SettingsPage } from './SettingsPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderSettings(deploymentId: string, initialPath?: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const settingsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/settings',
    component: () => <SettingsPage disableStream />,
  })
  const usersRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/users',
    component: () => <div data-testid="users-target" />,
  })
  const decommRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/decommission/$deploymentId',
    component: () => <div data-testid="decomm-target" />,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div data-testid="wizard-target" />,
  })
  // Sidebar's Link targets — register them so href resolution works.
  const provisionRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <div />,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div />,
  })
  const dashboardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/dashboard',
    component: () => <div />,
  })
  const cloudRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/cloud',
    component: () => <div />,
  })

  const tree = rootRoute.addChildren([
    settingsRoute,
    usersRoute,
    decommRoute,
    wizardRoute,
    provisionRoute,
    jobsRoute,
    dashboardRoute,
    cloudRoute,
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [initialPath ?? `/provision/${deploymentId}/settings`],
    }),
  })
  // SettingsPage mounts PortalShell (→ ReadinessChip, #3935) and calls
  // useResolvedDeploymentId — both are TanStack-Query consumers, so the
  // harness must supply a QueryClient exactly as src/main.tsx does.
  // Retries off + gcTime 0 so a failing stub fetch never stalls the test.
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  // jsdom doesn't ship EventSource. The page's own SSE attach is off via
  // `disableStream`, but the #settings-sovereign section renders
  // <SovereigntyCard /> (SettingsPage.tsx:451), whose useCutoverEvents
  // opens its own stream (SovereigntyCard.tsx:77 → useCutoverEvents.ts:278)
  // — that seam is `override`, not `disableStream`, so the shell needs a
  // shim. Same stub the hook's own suite installs
  // (useCutoverEvents.test.tsx:31-52).
  if (typeof (globalThis as unknown as { EventSource?: unknown }).EventSource === 'undefined') {
    class StubEventSource {
      url: string
      readyState = 0
      onopen: ((this: EventSource, ev: Event) => unknown) | null = null
      onmessage: ((this: EventSource, ev: MessageEvent) => unknown) | null = null
      onerror: ((this: EventSource, ev: Event) => unknown) | null = null
      static readonly CLOSED = 2
      constructor(url: string) {
        this.url = url
      }
      addEventListener(): void {}
      removeEventListener(): void {}
      close(): void {}
    }
    ;(globalThis as unknown as { EventSource: typeof EventSource }).EventSource =
      StubEventSource as unknown as typeof EventSource
  }
  // Prevent the snapshot fetch from polluting the test by serving an
  // empty events response with no terminal state. This isolates the
  // test from the network and lets us assert "no FQDN yet" rendering.
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => {
  cleanup()
  delete (globalThis as unknown as { EventSource?: unknown }).EventSource
})

const ALL_SECTIONS = [
  'organization',
  'sovereign',
  'api-tokens',
  'cloud-credentials',
  'dns',
  'domain-mode',
  // #4089: Parent Domains re-homed from the lone Settings sub-nav child
  // into a granular anchor section, consistent with #dns.
  'parent-domains',
  'marketplace',
  'notifications',
  'members',
  'danger-zone',
] as const

describe('SettingsPage — chrome', () => {
  it('renders inside PortalShell with the sidebar + page title', async () => {
    renderSettings('d-test-1234')
    expect(await screen.findByTestId('sov-portal-shell')).toBeTruthy()
    expect(await screen.findByTestId('admin-sidebar')).toBeTruthy()
    const title = await screen.findByTestId('portal-header-title')
    expect(title.textContent).toContain('Settings')
  })

  it('does NOT redirect to the wizard (regression guard for issue #516)', async () => {
    renderSettings('d-test-1234')
    // The page itself must mount — if it redirected to /wizard the
    // settings-page testid would be absent.
    expect(await screen.findByTestId('settings-page')).toBeTruthy()
    expect(screen.queryByTestId('wizard-target')).toBeNull()
  })
})

describe('SettingsPage — section catalogue', () => {
  it('renders all industry-standard sections (incl. #parent-domains, #4089)', async () => {
    renderSettings('d-test-1234')
    for (const id of ALL_SECTIONS) {
      expect(await screen.findByTestId(`settings-section-${id}`)).toBeTruthy()
      expect(screen.getByTestId(`settings-section-title-${id}`)).toBeTruthy()
    }
  })

  it('left-rail TOC has one entry per section, each targeting the matching anchor', async () => {
    renderSettings('d-test-1234')
    const toc = await screen.findByTestId('settings-toc')
    for (const id of ALL_SECTIONS) {
      const link = within(toc).getByTestId(`settings-toc-${id}`) as HTMLAnchorElement
      expect(link.getAttribute('href')).toBe(`#${id}`)
    }
  })
})

describe('SettingsPage — Organization section reflects wizard store', () => {
  it('renders the org name + email from the wizard store', async () => {
    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      orgName: 'Acme Corp',
      orgEmail: 'billing@acme.example',
    })
    renderSettings('d-test-1234')
    const name = await screen.findByTestId('settings-org-name')
    expect(name.textContent).toContain('Acme Corp')
    const email = screen.getByTestId('settings-org-email')
    expect(email.textContent).toContain('billing@acme.example')
  })
})

describe('SettingsPage — Sovereign section reflects deployment id', () => {
  it('renders the deployment id verbatim', async () => {
    renderSettings('d-omantel-7777')
    const node = await screen.findByTestId('settings-sov-deployment-id')
    expect(node.textContent).toContain('d-omantel-7777')
  })
})

describe('SettingsPage — API tokens', () => {
  it('renders the 5 canonical service tokens', async () => {
    renderSettings('d-test-1234')
    const list = await screen.findByTestId('settings-tokens-list')
    for (const id of ['catalyst-api', 'openbao', 'harbor', 'gitea', 'keycloak']) {
      expect(within(list).getByTestId(`settings-token-row-${id}`)).toBeTruthy()
      expect(within(list).getByTestId(`settings-token-revoke-${id}`)).toBeTruthy()
    }
  })

  it('marks the API tokens section as pending-api (no backend wired yet)', async () => {
    renderSettings('d-test-1234')
    const section = await screen.findByTestId('settings-section-api-tokens')
    expect(section.getAttribute('data-pending-api')).toBe('true')
    expect(screen.getByTestId('settings-pending-api-api-tokens')).toBeTruthy()
  })
})

describe('SettingsPage — Members links to User Access', () => {
  /**
   * 🛑 KNOWN-FAILING — this is a REAL defect, not a stale expectation.
   * Do NOT "fix" it by relaxing the regex to `/users`.
   *
   * SettingsPage renders on BOTH the mothership route
   * (/provision/$deploymentId/settings) and the chroot Sovereign route
   * (/settings) — see the SettingsPage.tsx docstring at the useParams
   * call. But the Members link hardcodes the chroot form:
   *
   *     SettingsPage.tsx:399   to={`/users` as never}
   *
   * The `as never` cast defeats TanStack Router's typed-route checking,
   * which is what would otherwise reject a target that can't resolve
   * under the current route. Twenty lines later the SAME file uses the
   * correct mode-safe pattern for its sibling CTA:
   *
   *     SettingsPage.tsx:420-422
   *       to="/decommission/$deploymentId" params={{ deploymentId }}
   *
   * Both routes are registered in the one tree — `/provision/$deploymentId/users`
   * (router.tsx:1112) and the chroot `/users` under consoleLayoutRoute
   * (router.tsx:1463-1466, mounted at router.tsx:2411). So on the
   * mothership this link navigates into SovereignConsoleLayout, where
   * useResolvedDeploymentId has no :deploymentId param and falls back to
   * /sovereign/self — which that hook's own doc comment states 404s on a
   * mothership host. Result: Members is a dead link on the mothership.
   *
   * This assertion has never run green: the file threw on a missing
   * QueryClient before reaching it, so the contradiction (present since
   * the earliest commit that has both files, a7fb48245) stayed masked.
   * Fixing the link is a navigation behaviour change and overlaps the
   * open route-gating decision in #5401, so it is left to that issue.
   */
  it('Members link points at /provision/$id/users', async () => {
    renderSettings('d-test-1234')
    const link = (await screen.findByTestId('settings-members-link')) as HTMLAnchorElement
    expect(link.getAttribute('href')).toMatch(/\/provision\/d-test-1234\/users$/)
  })
})

describe('SettingsPage — Danger zone', () => {
  it('Decommission CTA links to /decommission/$id', async () => {
    renderSettings('d-test-1234')
    const link = (await screen.findByTestId(
      'settings-danger-decommission-link',
    )) as HTMLAnchorElement
    expect(link.getAttribute('href')).toMatch(/\/decommission\/d-test-1234$/)
  })

  it('exposes wipe + transfer rows (pending-api stubs until backend wired)', async () => {
    renderSettings('d-test-1234')
    expect(await screen.findByTestId('settings-danger-wipe')).toBeTruthy()
    expect(screen.getByTestId('settings-danger-transfer')).toBeTruthy()
  })
})

describe('SettingsPage — DNS + Domain mode reflect wizard store', () => {
  it('renders pool domain + subdomain when set', async () => {
    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignDomainMode: 'pool',
      sovereignPoolDomain: 'omani-works',
      sovereignSubdomain: 'omantel',
    })
    renderSettings('d-test-1234')
    const pool = await screen.findByTestId('settings-dns-pool-domain')
    expect(pool.textContent).toContain('omani-works')
    const sub = screen.getByTestId('settings-dns-pool-subdomain')
    expect(sub.textContent).toContain('omantel')
    const mode = screen.getByTestId('settings-domain-mode-value')
    expect(mode.textContent).toContain('pool')
  })
})
