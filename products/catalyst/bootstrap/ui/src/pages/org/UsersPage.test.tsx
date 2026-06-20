/**
 * UsersPage.test.tsx — Organization-tier user CRUD page coverage (issue #802).
 *
 *   • Page heading + CTA render
 *   • Empty-state renders when no items
 *   • Populated table renders one row per user
 *   • The 3-step provisioning progress indicator renders
 *     pending/done/failed badges per the response shape
 */

import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { UsersPage } from './UsersPage'
import type { OrgUser } from './org.api'

afterEach(() => cleanup())

function user(overrides: Partial<OrgUser> = {}): OrgUser {
  return {
    uuid: 'uuid-1',
    email: 'alice@acme.example',
    state: 'done',
    created_at: '2026-05-04T12:00:00Z',
    updated_at: '2026-05-04T12:00:00Z',
    steps: { kc: 'done', newapi: 'done', secret: 'done' },
    ...overrides,
  }
}

describe('UsersPage', () => {
  it('renders heading + new-user CTA', () => {
    render(<UsersPage initialItems={[]} disableFetch />)
    expect(screen.getByText('Users')).toBeTruthy()
    expect(screen.getByTestId('org-users-new-cta')).toBeTruthy()
  })

  it('renders empty state when no users', () => {
    render(<UsersPage initialItems={[]} disableFetch />)
    expect(screen.getByTestId('org-users-empty')).toBeTruthy()
  })

  it('renders one row per user with all-done step indicators', () => {
    render(
      <UsersPage
        initialItems={[user(), user({ uuid: 'uuid-2', email: 'bob@acme.example' })]}
        disableFetch
      />,
    )
    expect(screen.getByTestId('org-users-row-uuid-1')).toBeTruthy()
    expect(screen.getByTestId('org-users-row-uuid-2')).toBeTruthy()
    // Each row carries 3 done step badges.
    const doneBadges = screen.getAllByTestId(/org-step-(keycloak|newapi|secret)-done/)
    expect(doneBadges.length).toBe(6)
  })

  it('renders pending/done/failed badges from response shape', () => {
    const partial: OrgUser = user({
      uuid: 'partial',
      state: 'failed',
      last_error: 'kc_create:transient:HTTP 502',
      steps: { kc: 'failed', newapi: 'pending', secret: 'pending' },
    })
    render(<UsersPage initialItems={[partial]} disableFetch />)
    expect(screen.getByTestId('org-step-keycloak-failed')).toBeTruthy()
    expect(screen.getByTestId('org-step-newapi-pending')).toBeTruthy()
    expect(screen.getByTestId('org-step-secret-pending')).toBeTruthy()
  })
})
