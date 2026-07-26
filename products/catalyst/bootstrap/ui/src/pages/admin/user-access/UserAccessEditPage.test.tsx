/**
 * UserAccessEditPage.test.tsx — form coverage (issue #323).
 *
 *   • Form fields render
 *   • Validation rejects missing user identity
 *   • Validation rejects missing sovereignRef / app
 *   • Submit happy-path calls the override and navigates to /users
 *   • Edit-mode disables the name field and pre-populates fields
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { UserAccessEditPage, validate } from './UserAccessEditPage'
import type { UserAccessItem, UserAccessRequest } from './userAccess.api'

interface RenderOpts {
  path: string
  initialItem?: UserAccessItem | null
  onSubmitOverride?: (req: UserAccessRequest) => Promise<void>
}

function renderEdit(opts: RenderOpts) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const newRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/users/new',
    component: () => (
      <UserAccessEditPage
        initialItem={opts.initialItem ?? null}
        disableFetch
        onSubmitOverride={opts.onSubmitOverride}
      />
    ),
  })
  const editRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/users/$name',
    component: () => (
      <UserAccessEditPage
        initialItem={opts.initialItem ?? null}
        disableFetch
        onSubmitOverride={opts.onSubmitOverride}
      />
    ),
  })
  const listRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/users',
    component: () => <div data-testid="users-list-target" />,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div data-testid="wizard-target" />,
  })
  const tree = rootRoute.addChildren([newRoute, editRoute, listRoute, wizardRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [opts.path] }),
  })
  // UserAccessEditPage mounts PortalShell, whose ReadinessChip (#3935)
  // and useResolvedDeploymentId are TanStack-Query consumers. Mirror the
  // src/main.tsx QueryClientProvider so the shell can render at all.
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

afterEach(() => cleanup())

describe('UserAccessEditPage — new', () => {
  it('renders all form fields', async () => {
    renderEdit({ path: '/provision/d-1/users/new' })
    expect(await screen.findByTestId('ua-input-name')).toBeTruthy()
    expect(screen.getByTestId('ua-input-subject')).toBeTruthy()
    expect(screen.getByTestId('ua-input-groups')).toBeTruthy()
    expect(screen.getByTestId('ua-input-sovereign')).toBeTruthy()
    expect(screen.getByTestId('ua-input-app')).toBeTruthy()
    expect(screen.getByTestId('ua-input-role')).toBeTruthy()
    expect(screen.getByTestId('ua-input-namespaces')).toBeTruthy()
    expect(screen.getByTestId('ua-button-save')).toBeTruthy()
  })

  it('happy-path submit calls the override + navigates to list', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined)
    renderEdit({ path: '/provision/d-1/users/new', onSubmitOverride: onSubmit })

    fireEvent.change(await screen.findByTestId('ua-input-name'), {
      target: { value: 'alice-helmwatch' },
    })
    fireEvent.change(screen.getByTestId('ua-input-subject'), {
      target: { value: 'alice' },
    })
    fireEvent.change(screen.getByTestId('ua-input-sovereign'), {
      target: { value: 'omantel' },
    })
    fireEvent.change(screen.getByTestId('ua-input-app'), {
      target: { value: 'helmwatch' },
    })
    fireEvent.change(screen.getByTestId('ua-input-namespaces'), {
      target: { value: 'helmwatch-prod, helmwatch-stg' },
    })
    fireEvent.click(screen.getByTestId('ua-button-save'))

    await waitFor(() => {
      expect(onSubmit).toHaveBeenCalledTimes(1)
    })
    const req = onSubmit.mock.calls[0][0] as UserAccessRequest
    expect(req.name).toBe('alice-helmwatch')
    expect(req.spec.user.keycloakSubject).toBe('alice')
    expect(req.spec.sovereignRef).toBe('omantel')
    expect(req.spec.applications).toHaveLength(1)
    expect(req.spec.applications[0].app).toBe('helmwatch')
    expect(req.spec.applications[0].role).toBe('editor')
    expect(req.spec.applications[0].namespaces).toEqual(['helmwatch-prod', 'helmwatch-stg'])
    // Navigated to list view
    await screen.findByTestId('users-list-target')
  })
})

describe('UserAccessEditPage — edit', () => {
  it('pre-populates fields from initialItem and disables name', async () => {
    const item: UserAccessItem = {
      name: 'alice-helmwatch',
      spec: {
        user: { keycloakSubject: 'alice' },
        sovereignRef: 'omantel',
        applications: [
          {
            app: 'helmwatch',
            role: 'editor',
            namespaces: ['helmwatch-prod'],
          },
        ],
      },
    }
    renderEdit({
      path: '/provision/d-1/users/alice-helmwatch',
      initialItem: item,
    })
    const nameInput = (await screen.findByTestId('ua-input-name')) as HTMLInputElement
    expect(nameInput.value).toBe('alice-helmwatch')
    expect(nameInput.disabled).toBe(true)
    const subjectInput = screen.getByTestId('ua-input-subject') as HTMLInputElement
    expect(subjectInput.value).toBe('alice')
    const sovInput = screen.getByTestId('ua-input-sovereign') as HTMLInputElement
    expect(sovInput.value).toBe('omantel')
    const appInput = screen.getByTestId('ua-input-app') as HTMLInputElement
    expect(appInput.value).toBe('helmwatch')
  })
})

describe('UserAccessEditPage — validation', () => {
  it('rejects missing user identity', () => {
    const res = validate(
      {
        name: 'alice',
        keycloakSubject: '',
        keycloakGroups: '',
        sovereignRef: 'omantel',
        app: 'helmwatch',
        role: 'editor',
        namespaces: '',
      },
      false,
    )
    expect(res).toMatch(/subject|group/i)
  })

  it('rejects missing sovereign ref', () => {
    const res = validate(
      {
        name: 'alice',
        keycloakSubject: 'alice',
        keycloakGroups: '',
        sovereignRef: '',
        app: 'helmwatch',
        role: 'editor',
        namespaces: '',
      },
      false,
    )
    expect(res).toMatch(/Sovereign/i)
  })

  it('rejects missing app', () => {
    const res = validate(
      {
        name: 'alice',
        keycloakSubject: 'alice',
        keycloakGroups: '',
        sovereignRef: 'omantel',
        app: '',
        role: 'editor',
        namespaces: '',
      },
      false,
    )
    expect(res).toMatch(/Application/i)
  })

  it('happy path returns null', () => {
    const res = validate(
      {
        name: 'alice',
        keycloakSubject: 'alice',
        keycloakGroups: '',
        sovereignRef: 'omantel',
        app: 'helmwatch',
        role: 'editor',
        namespaces: '',
      },
      false,
    )
    expect(res).toBeNull()
  })

  it('groups-only is also valid', () => {
    const res = validate(
      {
        name: 'ops',
        keycloakSubject: '',
        keycloakGroups: 'sovereign-ops',
        sovereignRef: 'omantel',
        app: 'helmwatch',
        role: 'viewer',
        namespaces: '',
      },
      false,
    )
    expect(res).toBeNull()
  })
})
