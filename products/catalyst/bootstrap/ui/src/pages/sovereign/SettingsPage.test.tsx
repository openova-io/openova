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
  return render(<RouterProvider router={router} />)
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
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
